package post

import (
	"context"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func (s *PostService) SchedulePost(
	ctx context.Context,
	req *connect.Request[managev1.SchedulePostRequest],
) (*connect.Response[managev1.PostLifecycleMutationResponse], error) {
	if req.Msg.ScheduledAt == nil {
		return nil, errs.Required("scheduled_at")
	}
	if err := req.Msg.ScheduledAt.CheckValid(); err != nil {
		return nil, errs.InvalidArgument("scheduled_at", "must be a valid timestamp")
	}
	scheduledAt := req.Msg.ScheduledAt.AsTime().UTC()
	if scheduledAt.Second() != 0 || scheduledAt.Nanosecond() != 0 {
		return nil, errs.InvalidArgument("scheduled_at", "must use minute precision")
	}
	timeZone := strings.TrimSpace(req.Msg.ScheduledTimeZone)
	if timeZone == "" {
		return nil, errs.Required("scheduled_time_zone")
	}
	if _, err := time.LoadLocation(timeZone); err != nil {
		return nil, errs.InvalidArgument("scheduled_time_zone", "must be a valid IANA time zone")
	}

	var post model.Post
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var databaseNow time.Time
		if err := tx.Raw("SELECT CURRENT_TIMESTAMP").Scan(&databaseNow).Error; err != nil {
			return errs.Internal(err)
		}
		if !scheduledAt.After(databaseNow.UTC()) {
			return errs.InvalidArgument("scheduled_at", "must be in the future")
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&post, "id = ?", req.Msg.Id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("post", req.Msg.Id)
			}
			return errs.Internal(err)
		}
		if _, err := requireLockedPostActionForStatus(ctx, tx, s.spiceDB, post.ID, post.Status, policyv1.Post.Publish); err != nil {
			return err
		}
		if post.Status != model.PostStatus(managev1.PostStatus_POST_STATUS_DRAFT.String()) &&
			post.Status != model.PostStatus(managev1.PostStatus_POST_STATUS_SCHEDULED.String()) {
			return errs.FailedPrecondition("only draft or scheduled posts can be scheduled")
		}
		previousStatus := post.Status
		if err := tx.Model(&post).Updates(structured.Fields{
			"status":              managev1.PostStatus_POST_STATUS_SCHEDULED.String(),
			"scheduled_at":        scheduledAt,
			"scheduled_time_zone": timeZone,
			"updated_at":          databaseNow.UTC(),
		}).Error; err != nil {
			return err
		}
		post.Status = model.PostStatus(managev1.PostStatus_POST_STATUS_SCHEDULED.String())
		post.ScheduledAt = &scheduledAt
		post.ScheduledTimeZone = &timeZone
		post.UpdatedAt = databaseNow.UTC()
		return s.appendPostScheduleAudit(ctx, tx, post.ID, previousStatus, post.Status, scheduledAt, timeZone)
	}); err != nil {
		return nil, err
	}
	response, err := s.postLifecycleMutationResponse(ctx, &post, true)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(response), nil
}

func (s *PostService) CancelPostSchedule(
	ctx context.Context,
	req *connect.Request[managev1.CancelPostScheduleRequest],
) (*connect.Response[managev1.PostLifecycleMutationResponse], error) {
	var post model.Post
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&post, "id = ?", req.Msg.Id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("post", req.Msg.Id)
			}
			return errs.Internal(err)
		}
		if _, err := requireLockedPostActionForStatus(ctx, tx, s.spiceDB, post.ID, post.Status, policyv1.Post.Publish); err != nil {
			return err
		}
		if post.Status != model.PostStatus(managev1.PostStatus_POST_STATUS_SCHEDULED.String()) {
			return errs.FailedPrecondition("only scheduled posts can have their schedule cancelled")
		}
		previousScheduledAt := post.ScheduledAt
		previousTimeZone := post.ScheduledTimeZone
		previousStatus := post.Status
		now := time.Now().UTC()
		if err := tx.Model(&post).Updates(structured.Fields{
			"status":              managev1.PostStatus_POST_STATUS_DRAFT.String(),
			"scheduled_at":        nil,
			"scheduled_time_zone": nil,
			"updated_at":          now,
		}).Error; err != nil {
			return err
		}
		if previousScheduledAt == nil || previousTimeZone == nil {
			return errs.InternalMsg("scheduled post is missing schedule details")
		}
		post.Status = model.PostStatus(managev1.PostStatus_POST_STATUS_DRAFT.String())
		post.ScheduledAt = nil
		post.ScheduledTimeZone = nil
		post.UpdatedAt = now
		return s.appendPostScheduleAudit(ctx, tx, post.ID, previousStatus, post.Status, *previousScheduledAt, *previousTimeZone)
	}); err != nil {
		return nil, err
	}
	response, err := s.postLifecycleMutationResponse(ctx, &post, true)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(response), nil
}

func (s *PostService) RepublishPost(
	ctx context.Context,
	req *connect.Request[managev1.RepublishPostRequest],
) (*connect.Response[managev1.PostLifecycleMutationResponse], error) {
	var post model.Post
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&post, "id = ?", req.Msg.Id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("post", req.Msg.Id)
			}
			return errs.Internal(err)
		}
		if _, err := requireLockedPostActionForStatus(ctx, tx, s.spiceDB, post.ID, post.Status, policyv1.Post.Publish); err != nil {
			return err
		}
		if post.Status != model.PostStatus(managev1.PostStatus_POST_STATUS_ARCHIVED.String()) {
			return errs.FailedPrecondition("only archived posts can be republished")
		}
		now := time.Now().UTC()
		previousStatus := post.Status
		updates := structured.Fields{
			"status":              managev1.PostStatus_POST_STATUS_PUBLISHED.String(),
			"scheduled_at":        nil,
			"scheduled_time_zone": nil,
			"updated_at":          now,
		}
		if post.PublishedAt == nil {
			updates["published_at"] = now
		}
		if err := tx.Model(&post).Updates(updates).Error; err != nil {
			return err
		}
		post.Status = model.PostStatus(managev1.PostStatus_POST_STATUS_PUBLISHED.String())
		post.ScheduledAt = nil
		post.ScheduledTimeZone = nil
		post.UpdatedAt = now
		if post.PublishedAt == nil {
			post.PublishedAt = &now
		}
		return s.appendPostLifecycleAudit(ctx, tx, post.ID, previousStatus, post.Status)
	}); err != nil {
		return nil, err
	}
	response, err := s.postLifecycleMutationResponse(ctx, &post, true)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(response), nil
}
