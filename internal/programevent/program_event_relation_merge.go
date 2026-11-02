package programevent

import (
	"context"
	"math"

	"github.com/echovisionlab/geul-api/internal/domainerr"
)

// relationNames is the request collection and ID field of each relation
// kind.
var relationNames = map[RelationKind]struct{ collection, idField string }{
	ArtistRelation: {"artists", "artist_id"},
	LabelRelation:  {"labels", "label_id"},
	ClientRelation: {"clients", "client_id"},
}

func mergeAndApplyProgramEventRelations(
	ctx context.Context,
	store Store,
	kind RelationKind,
	eventID string,
	observed, desired []Relation,
) (bool, error) {
	current, err := store.Relations(ctx, kind, eventID)
	if err != nil {
		return false, err
	}
	names := relationNames[kind]
	merged, err := mergeProgramEventRelationRows(names.collection, names.idField, current, observed, desired)
	if err != nil {
		return false, err
	}
	return applyProgramEventRelationDelta(ctx, store, kind, eventID, current, merged)
}

func mergeProgramEventRelationRows(
	collection, idField string,
	current []Relation,
	observed, desired []Relation,
) ([]Relation, error) {
	observedRows, err := indexProgramEventRelationInputs(collection, idField, observed)
	if err != nil {
		return nil, err
	}
	desiredRows, err := indexProgramEventRelationInputs(collection, idField, desired)
	if err != nil {
		return nil, err
	}

	currentIDs := make(map[string]struct{}, len(current))
	maxSortOrder := int32(-1)
	for _, row := range current {
		currentIDs[row.ID] = struct{}{}
		if row.SortOrder > maxSortOrder {
			maxSortOrder = row.SortOrder
		}
	}
	next := make([]Relation, 0, len(current)+len(desiredRows))
	for _, row := range current {
		observedRow, wasObserved := observedRows[row.ID]
		desiredRow, isDesired := desiredRows[row.ID]
		if wasObserved && !isDesired {
			continue
		}
		if wasObserved && isDesired {
			if role := desiredRow.Role; role != nil && !sameNullableString(observedRow.Role, role) {
				row.Role = role
			}
		}
		next = append(next, row)
	}

	for _, desiredRow := range desired {
		if _, wasObserved := observedRows[desiredRow.ID]; wasObserved {
			continue
		}
		if _, alreadyCurrent := currentIDs[desiredRow.ID]; alreadyCurrent {
			continue
		}
		if maxSortOrder == math.MaxInt32 {
			return nil, domainerr.InvalidField(collection, "sort order capacity exceeded")
		}
		maxSortOrder++
		next = append(next, Relation{ID: desiredRow.ID, Role: desiredRow.Role, SortOrder: maxSortOrder})
		currentIDs[desiredRow.ID] = struct{}{}
	}
	return next, nil
}

// applyProgramEventRelationDelta persists only changes between the locked
// current rows and the merged result. It intentionally never rewrites sort
// order for retained rows, since Event relation inputs currently have no
// explicit reorder control.
func applyProgramEventRelationDelta(
	ctx context.Context,
	store Store,
	kind RelationKind,
	eventID string,
	current, merged []Relation,
) (bool, error) {
	currentByID := make(map[string]Relation, len(current))
	for _, row := range current {
		currentByID[row.ID] = row
	}
	mergedByID := make(map[string]Relation, len(merged))
	for _, row := range merged {
		mergedByID[row.ID] = row
	}

	changed := false
	for _, row := range current {
		mergedRow, retained := mergedByID[row.ID]
		if !retained {
			rowsAffected, err := store.DeleteRelation(ctx, kind, eventID, row.ID)
			if err != nil {
				return false, err
			}
			changed = changed || rowsAffected > 0
			continue
		}
		if sameNullableString(row.Role, mergedRow.Role) {
			continue
		}
		rowsAffected, err := store.SetRelationRole(ctx, kind, eventID, row.ID, mergedRow.Role)
		if err != nil {
			return false, err
		}
		changed = changed || rowsAffected > 0
	}

	for _, row := range merged {
		if _, existed := currentByID[row.ID]; existed {
			continue
		}
		if err := store.InsertRelation(ctx, kind, eventID, row); err != nil {
			return false, err
		}
		changed = true
	}
	return changed, nil
}

func indexProgramEventRelationInputs(collection, idField string, rows []Relation) (map[string]Relation, error) {
	indexed := make(map[string]Relation, len(rows))
	for _, row := range rows {
		if row.ID == "" {
			return nil, domainerr.Required(idField)
		}
		if _, exists := indexed[row.ID]; exists {
			return nil, domainerr.InvalidField(collection, "duplicate relation id")
		}
		indexed[row.ID] = row
	}
	return indexed, nil
}
