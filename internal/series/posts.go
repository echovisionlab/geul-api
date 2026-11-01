package series

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type AssignPostToSeriesCommand struct{ PostID, SeriesID string }
type UnassignPostFromSeriesCommand struct{ PostID, SeriesID string }

// ReorderSeriesPostsCommand gives the Posts of a Series the order of
// PostIDs, which must name exactly its current Posts.
type ReorderSeriesPostsCommand struct {
	SeriesID string
	PostIDs  []string
}

func (s *Service) AssignPostToSeries(ctx context.Context, command AssignPostToSeriesCommand) error {
	var observed *string
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		observed, err = store.Posts().SeriesOf(ctx, command.PostID)
		return err
	}); err != nil {
		if errors.Is(err, ErrPostNotFound) {
			return domainerr.ResourceNotFound("post", command.PostID)
		}
		return err
	}
	var audits seriesAudits
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[struct{}, Store]{
		// Both Series roots are locked in ID order before the target's
		// manage check.
		Access: mutation.Locked(func(ctx context.Context, store Store, require mutation.Require) error {
			if err := lockSeriesAssignmentRoots(ctx, store, command.SeriesID, observed); err != nil {
				return err
			}
			return requireSeriesPermissionAndLock(ctx, store, require, command.SeriesID, policyv1.PostSeries.Manage)
		}),
		Change: func(ctx context.Context, store Store) (struct{}, error) {
			return struct{}{}, assignLockedPost(ctx, store, &audits, command.PostID, command.SeriesID, observed)
		},
		Audit: func(struct{}) mutation.Audit { return audits.audit() },
	})
	return err
}

// assignPostToSeriesAfterSeriesPermission assigns a Post inside a mutation
// that already holds the Series manage decision.
func assignPostToSeriesAfterSeriesPermission(
	ctx context.Context,
	store Store,
	audits *seriesAudits,
	postID string,
	seriesID string,
	observedSeriesID *string,
) error {
	if err := lockSeriesAssignmentRoots(ctx, store, seriesID, observedSeriesID); err != nil {
		return err
	}
	return assignLockedPost(ctx, store, audits, postID, seriesID, observedSeriesID)
}

// assignLockedPost moves a Post into seriesID once both Series roots are
// locked: the Post must still be where it was observed and its owner must
// allow the edit.
func assignLockedPost(
	ctx context.Context,
	store Store,
	audits *seriesAudits,
	postID string,
	seriesID string,
	observedSeriesID *string,
) error {
	posts := store.Posts()
	currentSeriesID, err := lockPostSeriesRelation(ctx, posts, postID)
	if err != nil {
		return err
	}
	if !equalOptionalString(currentSeriesID, observedSeriesID) {
		return domainerr.Precondition("post series relation changed; retry")
	}
	if err := posts.RequireLockedEdit(ctx, postID); err != nil {
		return err
	}
	if equalOptionalString(currentSeriesID, &seriesID) {
		return nil
	}
	nextOrder, err := posts.NextOrder(ctx, seriesID)
	if err != nil {
		return err
	}
	now := dbtime.Now()
	if err := posts.Assign(ctx, postID, seriesID, nextOrder, now); err != nil {
		if errors.Is(err, ErrPostNotFound) {
			return domainerr.ResourceNotFound("post", postID)
		}
		return err
	}
	previousSeriesID := ""
	if currentSeriesID != nil {
		previousSeriesID = *currentSeriesID
	}
	audits.membership(seriesID, postID, previousSeriesID, seriesID)
	if currentSeriesID == nil {
		return advanceSeriesContentDocumentForMutation(ctx, store, seriesID, time.Now().UTC())
	}
	if err := compactSeriesPostOrders(ctx, posts, *currentSeriesID, now); err != nil {
		return err
	}
	if err := advanceSeriesContentDocumentForMutation(ctx, store, seriesID, time.Now().UTC()); err != nil {
		return err
	}
	return advanceSeriesContentDocumentForMutation(ctx, store, *currentSeriesID, time.Now().UTC())
}

func lockSeriesAssignmentRoots(ctx context.Context, store Store, targetSeriesID string, currentSeriesID *string) error {
	seriesIDs := []string{targetSeriesID}
	if currentSeriesID != nil && *currentSeriesID != targetSeriesID {
		seriesIDs = append(seriesIDs, *currentSeriesID)
	}
	sort.Strings(seriesIDs)
	for _, seriesID := range seriesIDs {
		if err := lockSeriesRoot(ctx, store, seriesID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) UnassignPostFromSeries(ctx context.Context, command UnassignPostFromSeriesCommand) error {
	var audits seriesAudits
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[struct{}, Store]{
		Access: lockedSeries(command.SeriesID, policyv1.PostSeries.Manage),
		Change: func(ctx context.Context, store Store) (struct{}, error) {
			posts := store.Posts()
			postSeriesID, err := lockPostSeriesRelation(ctx, posts, command.PostID)
			if err != nil {
				return struct{}{}, err
			}
			if postSeriesID == nil || *postSeriesID != command.SeriesID {
				return struct{}{}, domainerr.InvalidField("post_id", "must belong to the requested series")
			}
			now := dbtime.Now()
			if err := seriesRelationError(posts.Clear(ctx, command.PostID, command.SeriesID, now)); err != nil {
				return struct{}{}, err
			}
			if err := compactSeriesPostOrders(ctx, posts, command.SeriesID, now); err != nil {
				return struct{}{}, err
			}
			audits.membership(command.SeriesID, command.PostID, command.SeriesID, "")
			return struct{}{}, advanceSeriesContentDocumentForMutation(ctx, store, command.SeriesID, time.Now().UTC())
		},
		Audit: func(struct{}) mutation.Audit { return audits.audit() },
	})
	return err
}

func (s *Service) ReorderSeriesPosts(ctx context.Context, command ReorderSeriesPostsCommand) error {
	postIDs, err := validateSeriesPostOrder(command.PostIDs)
	if err != nil {
		return err
	}
	var audits seriesAudits
	_, err = mutation.Run(ctx, s.mutations, mutation.StoreSpec[struct{}, Store]{
		Access: lockedSeries(command.SeriesID, policyv1.PostSeries.Manage),
		Change: func(ctx context.Context, store Store) (struct{}, error) {
			posts := store.Posts()
			if err := lockSeriesOrderPosts(ctx, posts, postIDs); err != nil {
				return struct{}{}, err
			}
			currentPostIDs, err := posts.SeriesPostIDs(ctx, command.SeriesID)
			if err != nil {
				return struct{}{}, err
			}
			if !sameStringSet(currentPostIDs, postIDs) {
				return struct{}{}, domainerr.InvalidField("post_ids", "must be the exact set of posts currently assigned to the series")
			}
			if sameSeriesPostOrder(currentPostIDs, postIDs) {
				return struct{}{}, nil
			}
			if err := orderSeriesPosts(ctx, posts, command.SeriesID, postIDs, dbtime.Now()); err != nil {
				return struct{}{}, err
			}
			audits.order(command.SeriesID, postIDs)
			return struct{}{}, advanceSeriesContentDocumentForMutation(ctx, store, command.SeriesID, time.Now().UTC())
		},
		Audit: func(struct{}) mutation.Audit { return audits.audit() },
	})
	return err
}

func sameSeriesPostOrder(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
