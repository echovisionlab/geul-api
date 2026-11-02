package post

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// SchedulePostCommand schedules a draft or scheduled Post for publication
// at a minute-precise instant in an IANA time zone.
type SchedulePostCommand struct {
	ID          string
	ScheduledAt *time.Time
	TimeZone    string
}

func (s *Service) SchedulePost(ctx context.Context, command SchedulePostCommand) (_ Lifecycle, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", command.ID))

	if command.ScheduledAt == nil {
		return Lifecycle{}, domainerr.Required("scheduled_at")
	}
	scheduledAt := command.ScheduledAt.UTC()
	if scheduledAt.Second() != 0 || scheduledAt.Nanosecond() != 0 {
		return Lifecycle{}, domainerr.InvalidField("scheduled_at", "must use minute precision")
	}
	timeZone := strings.TrimSpace(command.TimeZone)
	if timeZone == "" {
		return Lifecycle{}, domainerr.Required("scheduled_time_zone")
	}
	if _, err := time.LoadLocation(timeZone); err != nil {
		return Lifecycle{}, domainerr.InvalidField("scheduled_time_zone", "must be a valid IANA time zone").WithCause(err)
	}
	var databaseNow time.Time
	scheduled, _, err := runPostMutation(ctx, s, mutation.StoreSpec[Post, Store]{
		Access: mutation.Locked(func(ctx context.Context, store Store, require mutation.Require) error {
			var err error
			if databaseNow, err = store.DatabaseNow(ctx); err != nil {
				return err
			}
			if !scheduledAt.After(databaseNow.UTC()) {
				return domainerr.InvalidField("scheduled_at", "must be in the future")
			}
			status, err := lockPostAuthorizationRoot(ctx, store, command.ID)
			if err != nil {
				return err
			}
			_, err = requireLockedPostActionForStatus(require, command.ID, status, policyv1.Post.Publish)
			return err
		}),
	}, func(ctx context.Context, store Store, effects *postEffects) (Post, error) {
		stored, err := store.ReadPost(ctx, command.ID)
		if err != nil {
			return stored, err
		}
		if stored.Status != StatusDraft &&
			stored.Status != StatusScheduled {
			return stored, domainerr.Precondition("only draft or scheduled posts can be scheduled")
		}
		previousStatus := stored.Status
		if err := store.UpdateLifecycle(ctx, stored.ID, LifecycleChange{
			Status: StatusScheduled, UpdatedAt: databaseNow.UTC(), ScheduledAt: &scheduledAt, ScheduledTimeZone: &timeZone,
		}); err != nil {
			return stored, err
		}
		stored.Status = StatusScheduled
		stored.ScheduledAt = &scheduledAt
		stored.ScheduledTimeZone = &timeZone
		stored.UpdatedAt = databaseNow.UTC()
		postScheduleAudit(effects, stored.ID, previousStatus, stored.Status, scheduledAt, timeZone)
		return stored, nil
	})
	if err != nil {
		return Lifecycle{}, err
	}
	return s.postLifecycle(ctx, scheduled)
}

func (s *Service) CancelPostSchedule(ctx context.Context, command PostLifecycleCommand) (_ Lifecycle, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", command.ID))

	cancelled, _, err := runPostMutation(ctx, s, mutation.StoreSpec[Post, Store]{
		Access: lockedPost(command.ID, policyv1.Post.Publish, nil),
	}, func(ctx context.Context, store Store, effects *postEffects) (Post, error) {
		stored, err := store.ReadPost(ctx, command.ID)
		if err != nil {
			return stored, err
		}
		if stored.Status != StatusScheduled {
			return stored, domainerr.Precondition("only scheduled posts can have their schedule cancelled")
		}
		previousScheduledAt := stored.ScheduledAt
		previousTimeZone := stored.ScheduledTimeZone
		previousStatus := stored.Status
		now := dbtime.Now()
		if err := store.UpdateLifecycle(ctx, stored.ID, LifecycleChange{Status: StatusDraft, UpdatedAt: now, ClearSchedule: true}); err != nil {
			return stored, err
		}
		if previousScheduledAt == nil || previousTimeZone == nil {
			return stored, errors.New("scheduled post is missing schedule details")
		}
		stored.Status = StatusDraft
		stored.ScheduledAt = nil
		stored.ScheduledTimeZone = nil
		stored.UpdatedAt = now
		postScheduleAudit(effects, stored.ID, previousStatus, stored.Status, *previousScheduledAt, *previousTimeZone)
		return stored, nil
	})
	if err != nil {
		return Lifecycle{}, err
	}
	return s.postLifecycle(ctx, cancelled)
}
