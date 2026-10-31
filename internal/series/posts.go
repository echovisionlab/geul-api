package series

import (
	"context"
	"errors"
	"sort"
	"time"

	"gorm.io/gorm"

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
	observed, err := s.postMembership.ReadSeriesRelation(ctx, s.db, command.PostID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domainerr.ResourceNotFound("post", command.PostID)
		}
		return err
	}
	var audits seriesAudits
	_, err = mutation.Run(ctx, s.mutations, mutation.Spec[struct{}]{
		// Both Series roots are locked in ID order before the target's
		// manage check.
		Access: mutation.Locked(func(ctx context.Context, tx *gorm.DB, require mutation.Require) error {
			if err := lockSeriesAssignmentRoots(ctx, tx, command.SeriesID, observed); err != nil {
				return err
			}
			return requireSeriesPermissionAndLock(ctx, tx, require, command.SeriesID, policyv1.PostSeries.Manage)
		}),
		Change: func(ctx context.Context, tx *gorm.DB) (struct{}, error) {
			return struct{}{}, s.assignLockedPost(ctx, tx, &audits, command.PostID, command.SeriesID, observed)
		},
		Audit: func(struct{}) mutation.Audit { return audits.audit() },
	})
	return err
}

// assignPostToSeriesAfterSeriesPermissionWithDB assigns a Post inside a
// mutation that already holds the Series manage decision.
func (s *Service) assignPostToSeriesAfterSeriesPermissionWithDB(
	ctx context.Context,
	tx *gorm.DB,
	audits *seriesAudits,
	postID string,
	seriesID string,
	observedSeriesID *string,
) error {
	if err := lockSeriesAssignmentRoots(ctx, tx, seriesID, observedSeriesID); err != nil {
		return err
	}
	return s.assignLockedPost(ctx, tx, audits, postID, seriesID, observedSeriesID)
}

// assignLockedPost moves a Post into seriesID once both Series roots are
// locked: the Post must still be where it was observed and its owner must
// allow the edit.
func (s *Service) assignLockedPost(
	ctx context.Context,
	tx *gorm.DB,
	audits *seriesAudits,
	postID string,
	seriesID string,
	observedSeriesID *string,
) error {
	currentSeriesID, err := lockPostSeriesRelation(ctx, tx, s.postMembership, postID)
	if err != nil {
		return err
	}
	if !sameOptionalString(currentSeriesID, observedSeriesID) {
		return domainerr.Precondition("post series relation changed; retry")
	}
	if err := s.postAccess.RequireLockedEdit(ctx, tx, s.spiceDB, postID); err != nil {
		return err
	}
	if sameOptionalString(currentSeriesID, &seriesID) {
		return nil
	}
	nextOrder, err := s.postMembership.NextSeriesOrder(ctx, tx, seriesID)
	if err != nil {
		return err
	}
	now := dbtime.Now()
	if err := s.postMembership.AssignSeries(ctx, tx, postID, seriesID, nextOrder, now); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
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
		return advanceSeriesContentDocumentForMutation(ctx, tx, seriesID, time.Now().UTC())
	}
	if err := compactSeriesPostOrders(ctx, tx, s.postMembership, *currentSeriesID, now); err != nil {
		return err
	}
	if err := advanceSeriesContentDocumentForMutation(ctx, tx, seriesID, time.Now().UTC()); err != nil {
		return err
	}
	return advanceSeriesContentDocumentForMutation(ctx, tx, *currentSeriesID, time.Now().UTC())
}

func lockSeriesAssignmentRoots(ctx context.Context, tx *gorm.DB, targetSeriesID string, currentSeriesID *string) error {
	seriesIDs := []string{targetSeriesID}
	if currentSeriesID != nil && *currentSeriesID != targetSeriesID {
		seriesIDs = append(seriesIDs, *currentSeriesID)
	}
	sort.Strings(seriesIDs)
	for _, seriesID := range seriesIDs {
		if err := lockSeriesRoot(ctx, tx, seriesID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) UnassignPostFromSeries(ctx context.Context, command UnassignPostFromSeriesCommand) error {
	var audits seriesAudits
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[struct{}]{
		Access: lockedSeries(command.SeriesID, policyv1.PostSeries.Manage),
		Change: func(ctx context.Context, tx *gorm.DB) (struct{}, error) {
			postSeriesID, err := lockPostSeriesRelation(ctx, tx, s.postMembership, command.PostID)
			if err != nil {
				return struct{}{}, err
			}
			if postSeriesID == nil || *postSeriesID != command.SeriesID {
				return struct{}{}, domainerr.InvalidField("post_id", "must belong to the requested series")
			}
			now := dbtime.Now()
			if err := seriesRelationError(s.postMembership.ClearSeries(ctx, tx, command.PostID, command.SeriesID, now)); err != nil {
				return struct{}{}, err
			}
			if err := compactSeriesPostOrders(ctx, tx, s.postMembership, command.SeriesID, now); err != nil {
				return struct{}{}, err
			}
			audits.membership(command.SeriesID, command.PostID, command.SeriesID, "")
			return struct{}{}, advanceSeriesContentDocumentForMutation(ctx, tx, command.SeriesID, time.Now().UTC())
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
	_, err = mutation.Run(ctx, s.mutations, mutation.Spec[struct{}]{
		Access: lockedSeries(command.SeriesID, policyv1.PostSeries.Manage),
		Change: func(ctx context.Context, tx *gorm.DB) (struct{}, error) {
			if err := lockSeriesOrderPosts(ctx, tx, s.postMembership, postIDs); err != nil {
				return struct{}{}, err
			}
			currentPostIDs, err := s.postMembership.SeriesPostIDs(ctx, tx, command.SeriesID)
			if err != nil {
				return struct{}{}, err
			}
			if !sameStringSet(currentPostIDs, postIDs) {
				return struct{}{}, domainerr.InvalidField("post_ids", "must be the exact set of posts currently assigned to the series")
			}
			if sameSeriesPostOrder(currentPostIDs, postIDs) {
				return struct{}{}, nil
			}
			if err := orderSeriesPosts(ctx, tx, s.postMembership, command.SeriesID, postIDs, dbtime.Now()); err != nil {
				return struct{}{}, err
			}
			audits.order(command.SeriesID, postIDs)
			return struct{}{}, advanceSeriesContentDocumentForMutation(ctx, tx, command.SeriesID, time.Now().UTC())
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

// Helper methods
