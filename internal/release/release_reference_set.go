package release

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
)

// SetReleaseCategoriesCommand applies the desired Category IDs against the
// observed ones.
type SetReleaseCategoriesCommand struct {
	ReleaseID   string
	CategoryIDs []string
	Observed    *Snapshot[string]
}

// SetReleaseGenresCommand applies the desired Genre IDs against the observed
// ones.
type SetReleaseGenresCommand struct {
	ReleaseID string
	GenreIDs  []string
	Observed  *Snapshot[string]
}

// SetReleaseStylesCommand applies the desired Style IDs against the observed
// ones.
type SetReleaseStylesCommand struct {
	ReleaseID string
	StyleIDs  []string
	Observed  *Snapshot[string]
}

// SetReleaseCategories applies a Category set edit.
func (s *Service) SetReleaseCategories(ctx context.Context, command SetReleaseCategoriesCommand) error {
	return setReleaseReferenceSet(ctx, s, command.ReleaseID, releaseCategories, command.CategoryIDs, command.Observed,
		func(releaseID, categoryID string) *model.ReleaseCategory {
			return &model.ReleaseCategory{ReleaseID: releaseID, CategoryID: categoryID}
		})
}

// SetReleaseGenres applies a Genre set edit.
func (s *Service) SetReleaseGenres(ctx context.Context, command SetReleaseGenresCommand) error {
	return setReleaseReferenceSet(ctx, s, command.ReleaseID, releaseGenres, command.GenreIDs, command.Observed,
		func(releaseID, genreID string) *model.ReleaseGenre {
			return &model.ReleaseGenre{ReleaseID: releaseID, GenreID: genreID}
		})
}

// SetReleaseStyles applies a Style set edit.
func (s *Service) SetReleaseStyles(ctx context.Context, command SetReleaseStylesCommand) error {
	return setReleaseReferenceSet(ctx, s, command.ReleaseID, releaseStyles, command.StyleIDs, command.Observed,
		func(releaseID, styleID string) *model.ReleaseStyle {
			return &model.ReleaseStyle{ReleaseID: releaseID, StyleID: styleID}
		})
}

// releaseReferenceRelation is a Release relation to a catalog reference by
// ID: its name in audit records and signals, its table and its column.
type releaseReferenceRelation struct {
	name, table, column string
}

var (
	releaseCategories = releaseReferenceRelation{name: "categories", table: "release_category", column: "category_id"}
	releaseGenres     = releaseReferenceRelation{name: "genres", table: "release_genre", column: "genre_id"}
	releaseStyles     = releaseReferenceRelation{name: "styles", table: "release_style", column: "style_id"}
)

func setReleaseReferenceSet[T any](
	ctx context.Context,
	service *Service,
	releaseID string,
	relation releaseReferenceRelation,
	ids []string,
	observed *Snapshot[string],
	newReference func(releaseID, referenceID string) *T,
) error {
	return service.setReleaseRelation(ctx, releaseID, relation.name, func(_ context.Context, tx *gorm.DB) (bool, error) {
		if err := requireObservedRelationSnapshot(observed); err != nil {
			return false, err
		}
		var existing []string
		if err := tx.Table(relation.table).Where("release_id = ?", releaseID).Pluck(relation.column, &existing).Error; err != nil {
			return false, err
		}
		removed, added := mergeObservedIDs(existing, observed.Items, ids)
		if len(removed) == 0 && len(added) == 0 {
			return false, nil
		}
		if len(removed) > 0 {
			query := "DELETE FROM " + relation.table + " WHERE release_id = ? AND " + relation.column + " IN ?"
			if err := tx.Exec(query, releaseID, removed).Error; err != nil {
				return false, err
			}
		}
		for _, id := range added {
			if err := tx.Create(newReference(releaseID, id)).Error; err != nil {
				return false, err
			}
		}
		return true, nil
	})
}

func mergeObservedIDs(current, observed, desired []string) (removed, added []string) {
	observedSet := make(map[string]struct{}, len(observed))
	desiredSet := make(map[string]struct{}, len(desired))
	currentSet := make(map[string]struct{}, len(current))
	for _, id := range observed {
		observedSet[id] = struct{}{}
	}
	for _, id := range desired {
		desiredSet[id] = struct{}{}
	}
	for _, id := range current {
		currentSet[id] = struct{}{}
		if _, hadObserved := observedSet[id]; hadObserved {
			if _, stillDesired := desiredSet[id]; !stillDesired {
				removed = append(removed, id)
			}
		}
	}
	for _, id := range desired {
		if _, hadObserved := observedSet[id]; hadObserved {
			continue
		}
		if _, exists := currentSet[id]; !exists {
			added = append(added, id)
			currentSet[id] = struct{}{}
		}
	}
	return removed, added
}
