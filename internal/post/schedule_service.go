package post

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// SchedulePostCommand schedules a draft or scheduled Post for publication
// at a minute-precise instant in an IANA time zone.
type SchedulePostCommand struct {
	ID          string
	ScheduledAt *time.Time
	TimeZone    string
}

func (s *Service) SchedulePost(ctx context.Context, command SchedulePostCommand) (_ *managev1.PostLifecycleMutationResponse, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", command.ID))

	if command.ScheduledAt == nil {
		return nil, domainerr.Required("scheduled_at")
	}
	scheduledAt := command.ScheduledAt.UTC()
	if scheduledAt.Second() != 0 || scheduledAt.Nanosecond() != 0 {
		return nil, domainerr.InvalidField("scheduled_at", "must use minute precision")
	}
	timeZone := strings.TrimSpace(command.TimeZone)
	if timeZone == "" {
		return nil, domainerr.Required("scheduled_time_zone")
	}
	if _, err := time.LoadLocation(timeZone); err != nil {
		return nil, domainerr.InvalidField("scheduled_time_zone", "must be a valid IANA time zone").WithCause(err)
	}
	var databaseNow time.Time
	post, _, err := runPostMutation(ctx, s, mutation.Spec[model.Post]{
		Access: mutation.Locked(func(ctx context.Context, tx *gorm.DB, require mutation.Require) error {
			if err := tx.Raw("SELECT CURRENT_TIMESTAMP").Scan(&databaseNow).Error; err != nil {
				return err
			}
			if !scheduledAt.After(databaseNow.UTC()) {
				return domainerr.InvalidField("scheduled_at", "must be in the future")
			}
			status, err := lockPostAuthorizationRoot(ctx, tx, command.ID)
			if err != nil {
				return err
			}
			_, err = requireLockedPostActionForStatus(require, command.ID, status, policyv1.Post.Publish)
			return err
		}),
	}, func(ctx context.Context, tx *gorm.DB, effects *postEffects) (model.Post, error) {
		var post model.Post
		if err := tx.First(&post, "id = ?", command.ID).Error; err != nil {
			return post, err
		}
		if post.Status != model.PostStatusDraft &&
			post.Status != model.PostStatusScheduled {
			return post, domainerr.Precondition("only draft or scheduled posts can be scheduled")
		}
		previousStatus := post.Status
		if err := tx.Model(&post).Updates(structured.Fields{
			"status":              string(model.PostStatusScheduled),
			"scheduled_at":        scheduledAt,
			"scheduled_time_zone": timeZone,
			"updated_at":          databaseNow.UTC(),
		}).Error; err != nil {
			return post, err
		}
		post.Status = model.PostStatusScheduled
		post.ScheduledAt = &scheduledAt
		post.ScheduledTimeZone = &timeZone
		post.UpdatedAt = databaseNow.UTC()
		postScheduleAudit(effects, post.ID, previousStatus, post.Status, scheduledAt, timeZone)
		return post, nil
	})
	if err != nil {
		return nil, err
	}
	return s.postLifecycleMutationResponse(ctx, &post)
}

func (s *Service) CancelPostSchedule(ctx context.Context, command PostLifecycleCommand) (_ *managev1.PostLifecycleMutationResponse, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", command.ID))

	post, _, err := runPostMutation(ctx, s, mutation.Spec[model.Post]{
		Access: lockedPost(command.ID, policyv1.Post.Publish, nil),
	}, func(ctx context.Context, tx *gorm.DB, effects *postEffects) (model.Post, error) {
		var post model.Post
		if err := tx.First(&post, "id = ?", command.ID).Error; err != nil {
			return post, err
		}
		if post.Status != model.PostStatusScheduled {
			return post, domainerr.Precondition("only scheduled posts can have their schedule cancelled")
		}
		previousScheduledAt := post.ScheduledAt
		previousTimeZone := post.ScheduledTimeZone
		previousStatus := post.Status
		now := dbtime.Now()
		if err := tx.Model(&post).Updates(structured.Fields{
			"status":              string(model.PostStatusDraft),
			"scheduled_at":        nil,
			"scheduled_time_zone": nil,
			"updated_at":          now,
		}).Error; err != nil {
			return post, err
		}
		if previousScheduledAt == nil || previousTimeZone == nil {
			return post, errors.New("scheduled post is missing schedule details")
		}
		post.Status = model.PostStatusDraft
		post.ScheduledAt = nil
		post.ScheduledTimeZone = nil
		post.UpdatedAt = now
		postScheduleAudit(effects, post.ID, previousStatus, post.Status, *previousScheduledAt, *previousTimeZone)
		return post, nil
	})
	if err != nil {
		return nil, err
	}
	return s.postLifecycleMutationResponse(ctx, &post)
}
