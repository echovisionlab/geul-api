package series

import (
	"context"
	"sort"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// SeriesService implements the SeriesService Connect handler
func (s *SeriesService) AssignPostToSeries(
	ctx context.Context,
	req *connect.Request[managev1.AssignPostToSeriesRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	var observed struct {
		SeriesID *string `gorm:"column:series_id"`
	}
	if err := s.db.WithContext(ctx).Table("post").
		Select("series_id").Where("id = ?", req.Msg.PostId).Take(&observed).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("post", req.Msg.PostId)
		}
		return nil, errs.Internal(err)
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return s.assignPostToSeriesWithDB(ctx, tx, req.Msg.PostId, req.Msg.SeriesId, observed.SeriesID)
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
	currentSeriesID, err := lockPostSeriesRelation(ctx, tx, postID)
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
	nextOrder, err := nextSeriesPostOrder(tx, seriesID)
	if err != nil {
		return err
	}
	if err := updatePostSeriesAssignment(tx, postID, seriesID, nextOrder); err != nil {
		return err
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
	if err := compactSeriesPostOrders(ctx, tx, *currentSeriesID); err != nil {
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

func nextSeriesPostOrder(tx *gorm.DB, seriesID string) (int, error) {
	var maxOrder *int
	err := tx.Table("post").Where("series_id = ?", seriesID).
		Select("MAX(series_order)").Scan(&maxOrder).Error
	if err != nil {
		return 0, errs.Internal(err)
	}
	if maxOrder == nil {
		return 0, nil
	}
	return *maxOrder + 1, nil
}

func updatePostSeriesAssignment(tx *gorm.DB, postID, seriesID string, order int) error {
	result := tx.Table("post").Where("id = ?", postID).
		Updates(structured.Fields{"series_id": seriesID, "series_order": order})
	if result.Error != nil {
		return errs.Internal(result.Error)
	}
	if result.RowsAffected != 1 {
		return errs.NotFound("post", postID)
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
		postSeriesID, err := lockPostSeriesRelation(ctx, tx, req.Msg.PostId)
		if err != nil {
			return err
		}
		if postSeriesID == nil || *postSeriesID != req.Msg.SeriesId {
			return errs.InvalidArgument("post_id", "must belong to the requested series")
		}
		result := tx.Table("post").
			Where("id = ? AND series_id = ?", req.Msg.PostId, req.Msg.SeriesId).
			Updates(structured.Fields{"series_id": nil, "series_order": nil})
		if result.Error != nil {
			return errs.Internal(result.Error)
		}
		if result.RowsAffected != 1 {
			return errs.FailedPrecondition("post series relation changed; retry")
		}
		if err := compactSeriesPostOrders(ctx, tx, req.Msg.SeriesId); err != nil {
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
		if err := lockSeriesOrderPosts(ctx, tx, postIDs); err != nil {
			return err
		}
		var currentPostIDs []string
		if err := tx.Table("post").
			Where("series_id = ?", req.Msg.SeriesId).
			Order("series_order ASC, id ASC").
			Pluck("id", &currentPostIDs).Error; err != nil {
			return errs.Internal(err)
		}
		if !sameStringSet(currentPostIDs, postIDs) {
			return errs.InvalidArgument("post_ids", "must be the exact set of posts currently assigned to the series")
		}
		if sameSeriesPostOrder(currentPostIDs, postIDs) {
			return nil
		}
		for index, postID := range postIDs {
			result := tx.Table("post").
				Where("id = ? AND series_id = ?", postID, req.Msg.SeriesId).
				Update("series_order", index)
			if result.Error != nil {
				return errs.Internal(result.Error)
			}
			if result.RowsAffected != 1 {
				return errs.FailedPrecondition("post series relation changed; retry")
			}
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
