package worker

import (
	"context"
	stderrors "errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/model"
)

func (h *Handlers) handleProcessUserDeletions(ctx context.Context) error {
	now := time.Now().UTC()
	scrubbed, err := member.ScrubExpiredMemberEmailProjections(ctx, h.db, now)
	if err != nil {
		return fmt.Errorf("scrub expired Member email projections: %w", err)
	}
	if scrubbed > 0 {
		slog.Info("Scrubbed expired Member email projections", "count", scrubbed)
	}
	identityPublisher, publisherOK := any(h.publisher).(account.UserDeletionIdentityDispatchPublisher)
	if !publisherOK {
		return fmt.Errorf("user deletion identity publisher is not configured")
	}
	unonboardedQueued, unonboardedErr := account.EnqueueExpiredUnonboardedMembers(
		ctx, h.db, identityPublisher, h.memberDeletion, now, account.UnonboardedMemberCleanupBatchSize,
	)
	if unonboardedQueued > 0 {
		slog.Info("Queued expired unonboarded Member hard deletions", "count", unonboardedQueued)
	}
	deletionPublisher, ok := h.publisher.(account.UserDeletionDispatchPublisher)
	if !ok {
		return fmt.Errorf("user deletion command publisher is not configured")
	}

	// Find deletion requests that have been scheduled and are past the grace period
	var requests []model.UserDeletionRequest
	if err := h.db.WithContext(ctx).
		Where("lifecycle_state IN ?", []string{"scheduled", "recovery_confirmation_pending"}).
		Where("scheduled_at < ?", now).
		Find(&requests).Error; err != nil {
		return err
	}

	if len(requests) == 0 {
		return unonboardedErr
	}

	slog.Info("Processing scheduled user deletions", "count", len(requests))

	var processedCount int
	for _, req := range requests {
		if err := account.DispatchScheduledUserDeletion(ctx, h.db, deletionPublisher, h.spicedbClient, h.memberDeletion, req.ID, now); err != nil {
			if stderrors.Is(err, account.ErrLastActiveAdminDeletion) {
				slog.Warn("Scheduled member deletion blocked", "member_id", req.MemberID, "identity_id", req.IdentityID, "reason", "last_active_admin")
				continue
			}
			slog.Warn("Member deletion command publish failed", "member_id", req.MemberID, "identity_id", req.IdentityID, "error", err)
			continue
		}

		processedCount++
		slog.Info("Member deletion command accepted", "member_id", req.MemberID, "identity_id", req.IdentityID)
	}

	slog.Info("Completed scheduled user deletions",
		"processed", processedCount,
		"total", len(requests))

	return unonboardedErr
}
