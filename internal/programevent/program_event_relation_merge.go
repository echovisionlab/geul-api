package programevent

import (
	"context"
	"math"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

func mergeProgramEventArtists(
	current []model.ProgramEventArtist,
	observed, desired []*managev1.ProgramEventArtist,
) ([]model.ProgramEventArtist, error) {
	return mergeProgramEventRelationRows(
		"artists", "artist_id", current, observed, desired,
		func(row *managev1.ProgramEventArtist) string { return row.GetArtistId() },
		func(row *managev1.ProgramEventArtist) *string { return row.Role },
		func(row model.ProgramEventArtist) string { return row.ArtistID },
		func(row *model.ProgramEventArtist, role *string) { row.Role = role },
		func(row *managev1.ProgramEventArtist, sortOrder int32) model.ProgramEventArtist {
			return model.ProgramEventArtist{ArtistID: row.GetArtistId(), Role: row.Role, SortOrder: sortOrder}
		},
		func(row model.ProgramEventArtist) int32 { return row.SortOrder },
	)
}

func mergeProgramEventLabels(
	current []model.ProgramEventLabel,
	observed, desired []*managev1.ProgramEventLabel,
) ([]model.ProgramEventLabel, error) {
	return mergeProgramEventRelationRows(
		"labels", "label_id", current, observed, desired,
		func(row *managev1.ProgramEventLabel) string { return row.GetLabelId() },
		func(row *managev1.ProgramEventLabel) *string { return row.Role },
		func(row model.ProgramEventLabel) string { return row.LabelID },
		func(row *model.ProgramEventLabel, role *string) { row.Role = role },
		func(row *managev1.ProgramEventLabel, sortOrder int32) model.ProgramEventLabel {
			return model.ProgramEventLabel{LabelID: row.GetLabelId(), Role: row.Role, SortOrder: sortOrder}
		},
		func(row model.ProgramEventLabel) int32 { return row.SortOrder },
	)
}

func mergeProgramEventClients(
	current []model.ProgramEventClient,
	observed, desired []*managev1.ProgramEventClient,
) ([]model.ProgramEventClient, error) {
	return mergeProgramEventRelationRows(
		"clients", "client_id", current, observed, desired,
		func(row *managev1.ProgramEventClient) string { return row.GetClientId() },
		func(row *managev1.ProgramEventClient) *string { return row.Role },
		func(row model.ProgramEventClient) string { return row.ClientID },
		func(row *model.ProgramEventClient, role *string) { row.Role = role },
		func(row *managev1.ProgramEventClient, sortOrder int32) model.ProgramEventClient {
			return model.ProgramEventClient{ClientID: row.GetClientId(), Role: row.Role, SortOrder: sortOrder}
		},
		func(row model.ProgramEventClient) int32 { return row.SortOrder },
	)
}

func mergeAndApplyProgramEventArtists(
	ctx context.Context,
	tx *gorm.DB,
	eventID string,
	observed, desired []*managev1.ProgramEventArtist,
) (bool, error) {
	current, err := loadProgramEventRelationRows[model.ProgramEventArtist](ctx, tx, eventID, "sort_order ASC, artist_id ASC")
	if err != nil {
		return false, errs.Internal(err)
	}
	merged, err := mergeProgramEventArtists(current, observed, desired)
	if err != nil {
		return false, err
	}
	return applyProgramEventRelationDelta(ctx, tx, eventID, current, merged,
		func(row model.ProgramEventArtist) string { return row.ArtistID },
		func(row model.ProgramEventArtist) *string { return row.Role },
		func(row *model.ProgramEventArtist, id string) { row.EventID = id },
		func(ctx context.Context, tx *gorm.DB, eventID, relationID string) (int64, error) {
			result := tx.WithContext(ctx).Where("event_id = ? AND artist_id = ?", eventID, relationID).Delete(&model.ProgramEventArtist{})
			return result.RowsAffected, result.Error
		},
		func(ctx context.Context, tx *gorm.DB, eventID, relationID string, role *string) (int64, error) {
			result := tx.WithContext(ctx).Model(&model.ProgramEventArtist{}).Where("event_id = ? AND artist_id = ?", eventID, relationID).Updates(map[string]any{"role": role})
			return result.RowsAffected, result.Error
		},
	)
}

func mergeAndApplyProgramEventLabels(
	ctx context.Context,
	tx *gorm.DB,
	eventID string,
	observed, desired []*managev1.ProgramEventLabel,
) (bool, error) {
	current, err := loadProgramEventRelationRows[model.ProgramEventLabel](ctx, tx, eventID, "sort_order ASC, label_id ASC")
	if err != nil {
		return false, errs.Internal(err)
	}
	merged, err := mergeProgramEventLabels(current, observed, desired)
	if err != nil {
		return false, err
	}
	return applyProgramEventRelationDelta(ctx, tx, eventID, current, merged,
		func(row model.ProgramEventLabel) string { return row.LabelID },
		func(row model.ProgramEventLabel) *string { return row.Role },
		func(row *model.ProgramEventLabel, id string) { row.EventID = id },
		func(ctx context.Context, tx *gorm.DB, eventID, relationID string) (int64, error) {
			result := tx.WithContext(ctx).Where("event_id = ? AND label_id = ?", eventID, relationID).Delete(&model.ProgramEventLabel{})
			return result.RowsAffected, result.Error
		},
		func(ctx context.Context, tx *gorm.DB, eventID, relationID string, role *string) (int64, error) {
			result := tx.WithContext(ctx).Model(&model.ProgramEventLabel{}).Where("event_id = ? AND label_id = ?", eventID, relationID).Updates(map[string]any{"role": role})
			return result.RowsAffected, result.Error
		},
	)
}

func mergeAndApplyProgramEventClients(
	ctx context.Context,
	tx *gorm.DB,
	eventID string,
	observed, desired []*managev1.ProgramEventClient,
) (bool, error) {
	current, err := loadProgramEventRelationRows[model.ProgramEventClient](ctx, tx, eventID, "sort_order ASC, client_id ASC")
	if err != nil {
		return false, errs.Internal(err)
	}
	merged, err := mergeProgramEventClients(current, observed, desired)
	if err != nil {
		return false, err
	}
	return applyProgramEventRelationDelta(ctx, tx, eventID, current, merged,
		func(row model.ProgramEventClient) string { return row.ClientID },
		func(row model.ProgramEventClient) *string { return row.Role },
		func(row *model.ProgramEventClient, id string) { row.EventID = id },
		func(ctx context.Context, tx *gorm.DB, eventID, relationID string) (int64, error) {
			result := tx.WithContext(ctx).Where("event_id = ? AND client_id = ?", eventID, relationID).Delete(&model.ProgramEventClient{})
			return result.RowsAffected, result.Error
		},
		func(ctx context.Context, tx *gorm.DB, eventID, relationID string, role *string) (int64, error) {
			result := tx.WithContext(ctx).Model(&model.ProgramEventClient{}).Where("event_id = ? AND client_id = ?", eventID, relationID).Updates(map[string]any{"role": role})
			return result.RowsAffected, result.Error
		},
	)
}

func loadProgramEventRelationRows[Row any](ctx context.Context, tx *gorm.DB, eventID, order string) ([]Row, error) {
	var rows []Row
	if err := tx.WithContext(ctx).Where("event_id = ?", eventID).Order(order).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func mergeProgramEventRelationRows[Input any, Row any](
	collection, idField string,
	current []Row,
	observed, desired []*Input,
	inputID func(*Input) string,
	inputRole func(*Input) *string,
	rowID func(Row) string,
	setRowRole func(*Row, *string),
	buildNew func(*Input, int32) Row,
	rowSortOrder func(Row) int32,
) ([]Row, error) {
	observedRows, err := indexProgramEventRelationInputs(collection, idField, observed, inputID)
	if err != nil {
		return nil, err
	}
	desiredRows, err := indexProgramEventRelationInputs(collection, idField, desired, inputID)
	if err != nil {
		return nil, err
	}

	currentIDs := make(map[string]struct{}, len(current))
	maxSortOrder := int32(-1)
	for _, row := range current {
		currentIDs[rowID(row)] = struct{}{}
		if sortOrder := rowSortOrder(row); sortOrder > maxSortOrder {
			maxSortOrder = sortOrder
		}
	}
	next := make([]Row, 0, len(current)+len(desiredRows))
	for _, row := range current {
		id := rowID(row)
		observedRow, wasObserved := observedRows[id]
		desiredRow, isDesired := desiredRows[id]
		if wasObserved && !isDesired {
			continue
		}
		if wasObserved && isDesired {
			role := inputRole(desiredRow)
			if role != nil && !sameNullableString(inputRole(observedRow), role) {
				setRowRole(&row, role)
			}
		}
		next = append(next, row)
	}

	for _, desiredRow := range desired {
		id := inputID(desiredRow)
		if _, wasObserved := observedRows[id]; wasObserved {
			continue
		}
		if _, alreadyCurrent := currentIDs[id]; alreadyCurrent {
			continue
		}
		if maxSortOrder == math.MaxInt32 {
			return nil, errs.InvalidArgument(collection, "sort order capacity exceeded")
		}
		maxSortOrder++
		next = append(next, buildNew(desiredRow, maxSortOrder))
		currentIDs[id] = struct{}{}
	}
	return next, nil
}

// applyProgramEventRelationDelta persists only changes between the locked
// current rows and the merged result. It intentionally never rewrites sort
// order for retained rows, since Event relation inputs currently have no
// explicit reorder control.
func applyProgramEventRelationDelta[Row any](
	ctx context.Context,
	tx *gorm.DB,
	eventID string,
	current, merged []Row,
	id func(Row) string,
	role func(Row) *string,
	setEventID func(*Row, string),
	delete func(context.Context, *gorm.DB, string, string) (int64, error),
	updateRole func(context.Context, *gorm.DB, string, string, *string) (int64, error),
) (bool, error) {
	currentByID := make(map[string]Row, len(current))
	for _, row := range current {
		currentByID[id(row)] = row
	}
	mergedByID := make(map[string]Row, len(merged))
	for _, row := range merged {
		mergedByID[id(row)] = row
	}

	changed := false
	for _, row := range current {
		rowID := id(row)
		mergedRow, retained := mergedByID[rowID]
		if !retained {
			rowsAffected, err := delete(ctx, tx, eventID, rowID)
			if err != nil {
				return false, errs.Internal(err)
			}
			changed = changed || rowsAffected > 0
			continue
		}
		if sameNullableString(role(row), role(mergedRow)) {
			continue
		}
		rowsAffected, err := updateRole(ctx, tx, eventID, rowID, role(mergedRow))
		if err != nil {
			return false, errs.Internal(err)
		}
		changed = changed || rowsAffected > 0
	}

	for _, row := range merged {
		if _, existed := currentByID[id(row)]; existed {
			continue
		}
		setEventID(&row, eventID)
		if err := tx.WithContext(ctx).Create(&row).Error; err != nil {
			return false, errs.Internal(err)
		}
		changed = true
	}
	return changed, nil
}

func indexProgramEventRelationInputs[Input any](
	collection, idField string,
	rows []*Input,
	inputID func(*Input) string,
) (map[string]*Input, error) {
	indexed := make(map[string]*Input, len(rows))
	for _, row := range rows {
		if row == nil {
			return nil, errs.InvalidArgument(collection, "relation rows must not be null")
		}
		id := inputID(row)
		if id == "" {
			return nil, errs.Required(idField)
		}
		if _, exists := indexed[id]; exists {
			return nil, errs.InvalidArgument(collection, "duplicate relation id")
		}
		indexed[id] = row
	}
	return indexed, nil
}
