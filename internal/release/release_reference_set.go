package release

import (
	"context"

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
	return s.setReleaseReferenceSet(ctx, command.ReleaseID, releaseCategories, command.CategoryIDs, command.Observed)
}

// SetReleaseGenres applies a Genre set edit.
func (s *Service) SetReleaseGenres(ctx context.Context, command SetReleaseGenresCommand) error {
	return s.setReleaseReferenceSet(ctx, command.ReleaseID, releaseGenres, command.GenreIDs, command.Observed)
}

// SetReleaseStyles applies a Style set edit.
func (s *Service) SetReleaseStyles(ctx context.Context, command SetReleaseStylesCommand) error {
	return s.setReleaseReferenceSet(ctx, command.ReleaseID, releaseStyles, command.StyleIDs, command.Observed)
}

// releaseReferenceRelation is a Release relation to a catalog reference by
// ID and its name in audit records and signals.
type releaseReferenceRelation struct {
	relation ReferenceRelation
	name     string
}

var (
	releaseCategories = releaseReferenceRelation{relation: ReferenceCategories, name: "categories"}
	releaseGenres     = releaseReferenceRelation{relation: ReferenceGenres, name: "genres"}
	releaseStyles     = releaseReferenceRelation{relation: ReferenceStyles, name: "styles"}
)

func (s *Service) setReleaseReferenceSet(
	ctx context.Context,
	releaseID string,
	relation releaseReferenceRelation,
	ids []string,
	observed *Snapshot[string],
) error {
	return s.setReleaseRelation(ctx, releaseID, relation.name, func(ctx context.Context, store Store) (bool, error) {
		if err := requireObservedRelationSnapshot(observed); err != nil {
			return false, err
		}
		existing, err := store.ReferenceIDs(ctx, relation.relation, releaseID)
		if err != nil {
			return false, err
		}
		removed, added := mergeObservedIDs(existing, observed.Items, ids)
		if len(removed) == 0 && len(added) == 0 {
			return false, nil
		}
		return true, store.ReplaceReferences(ctx, relation.relation, releaseID, removed, added)
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
