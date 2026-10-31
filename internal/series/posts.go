package series

import (
	"context"
	"errors"
	"sort"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// SeriesService implements the SeriesService Connect handler
func (s *SeriesService) AssignPostToSeries(
	ctx context.Context,
	req *connect.Request[managev1.AssignPostToSeriesRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	observed, err := s.postMembership.ReadSeriesRelation(ctx, s.db, req.Msg.PostId)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.NotFound("post", req.Msg.PostId)
		}
		return nil, errs.Internal(err)
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return s.assignPostToSeriesWithDB(ctx, tx, req.Msg.PostId, req.Msg.SeriesId, observed)
	}); err != nil {
		return nil, err
	}

	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
}

func (s *SeriesService) assignPostToSeriesWithDB(
	ctx context.Context,
	tx *gorm.DB,
	postID string,
	seriesID string,
	observedSeriesID *string,
) error {
	return s.assignPostToSeriesWithAuthorization(ctx, tx, postID, seriesID, observedSeriesID, true)
}

func (s *SeriesService) assignPostToSeriesAfterSeriesPermissionWithDB(
	ctx context.Context,
	tx *gorm.DB,
	postID string,
	seriesID string,
	observedSeriesID *string,
) error {
	return s.assignPostToSeriesWithAuthorization(ctx, tx, postID, seriesID, observedSeriesID, false)
}

func (s *SeriesService) assignPostToSeriesWithAuthorization(
	ctx context.Context,
	tx *gorm.DB,
	postID string,
	seriesID string,
	observedSeriesID *string,
	requireSeriesPermission bool,
) error {
	if err := lockSeriesAssignmentRoots(ctx, tx, seriesID, observedSeriesID); err != nil {
		return err
	}
	if requireSeriesPermission {
		if err := s.requireAssignableSeries(ctx, tx, seriesID); err != nil {
			return err
		}
	}
	currentSeriesID, err := lockPostSeriesRelation(ctx, tx, s.postMembership, postID)
	if err != nil {
		return err
	}
	if !sameOptionalString(currentSeriesID, observedSeriesID) {
		return errs.FailedPrecondition("post series relation changed; retry")
	}
	if err := s.postAccess.RequireLockedEdit(ctx, tx, s.spiceDB, postID); err != nil {
		return err
	}
	if sameOptionalString(currentSeriesID, &seriesID) {
		return nil
	}
	nextOrder, err := s.postMembership.NextSeriesOrder(ctx, tx, seriesID)
	if err != nil {
		return errs.Internal(err)
	}
	now := dbtime.Now()
	if err := s.postMembership.AssignSeries(ctx, tx, postID, seriesID, nextOrder, now); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.NotFound("post", postID)
		}
		return errs.Internal(err)
	}
	previousSeriesID := ""
	if currentSeriesID != nil {
		previousSeriesID = *currentSeriesID
	}
	if err := s.appendPostSeriesMembershipAudit(ctx, tx, seriesID, postID, previousSeriesID, seriesID); err != nil {
		return err
	}
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

// UnassignPostFromSeries removes a post from its series
func (s *SeriesService) UnassignPostFromSeries(
	ctx context.Context,
	req *connect.Request[managev1.UnassignPostFromSeriesRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.requireSeriesManageAndLock(ctx, tx, req.Msg.SeriesId); err != nil {
			return err
		}
		postSeriesID, err := lockPostSeriesRelation(ctx, tx, s.postMembership, req.Msg.PostId)
		if err != nil {
			return err
		}
		if postSeriesID == nil || *postSeriesID != req.Msg.SeriesId {
			return errs.InvalidArgument("post_id", "must belong to the requested series")
		}
		now := dbtime.Now()
		if err := seriesRelationError(s.postMembership.ClearSeries(ctx, tx, req.Msg.PostId, req.Msg.SeriesId, now)); err != nil {
			return err
		}
		if err := compactSeriesPostOrders(ctx, tx, s.postMembership, req.Msg.SeriesId, now); err != nil {
			return err
		}
		if err := s.appendPostSeriesMembershipAudit(ctx, tx, req.Msg.SeriesId, req.Msg.PostId, req.Msg.SeriesId, ""); err != nil {
			return err
		}
		return advanceSeriesContentDocumentForMutation(ctx, tx, req.Msg.SeriesId, time.Now().UTC())
	}); err != nil {
		return nil, err
	}

	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
}

// ReorderSeriesPosts reorders posts in a series
func (s *SeriesService) ReorderSeriesPosts(
	ctx context.Context,
	req *connect.Request[managev1.ReorderSeriesPostsRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	postIDs, err := validateSeriesPostOrder(req.Msg.PostIds)
	if err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.requireSeriesManageAndLock(ctx, tx, req.Msg.SeriesId); err != nil {
			return err
		}
		if err := lockSeriesOrderPosts(ctx, tx, s.postMembership, postIDs); err != nil {
			return err
		}
		currentPostIDs, err := s.postMembership.SeriesPostIDs(ctx, tx, req.Msg.SeriesId)
		if err != nil {
			return errs.Internal(err)
		}
		if !sameStringSet(currentPostIDs, postIDs) {
			return errs.InvalidArgument("post_ids", "must be the exact set of posts currently assigned to the series")
		}
		if sameSeriesPostOrder(currentPostIDs, postIDs) {
			return nil
		}
		if err := orderSeriesPosts(ctx, tx, s.postMembership, req.Msg.SeriesId, postIDs, dbtime.Now()); err != nil {
			return err
		}
		if err := s.appendPostSeriesOrderAudit(ctx, tx, req.Msg.SeriesId, postIDs); err != nil {
			return err
		}
		return advanceSeriesContentDocumentForMutation(ctx, tx, req.Msg.SeriesId, time.Now().UTC())
	}); err != nil {
		return nil, err
	}

	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
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
