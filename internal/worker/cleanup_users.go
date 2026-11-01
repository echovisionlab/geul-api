package worker

import (
	"context"
	stderrors "errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/account"
	accountpostgres "github.com/echovisionlab/geul-api/internal/account/postgres"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	memberpostgres "github.com/echovisionlab/geul-api/internal/member/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
)

func (h *Handlers) handleProcessUserDeletions(ctx context.Context) error {
	now := time.Now().UTC()
	scrubbed, err := memberpostgres.ScrubExpiredMemberEmailProjections(ctx, h.db, now)
	if err != nil {
		return fmt.Errorf("scrub expired Member email projections: %w", err)
	}
	if scrubbed > 0 {
		slog.InfoContext(ctx, "Scrubbed expired Member email projections", "count", scrubbed)
	}
	identityPublisher, publisherOK := any(h.publisher).(accountpostgres.UserDeletionIdentityDispatchPublisher)
	if !publisherOK {
		return fmt.Errorf("user deletion identity publisher is not configured")
	}
	unonboardedQueued, unonboardedErr := accountpostgres.EnqueueExpiredUnonboardedMembers(
		ctx, h.db, identityPublisher, h.memberDeletion, now, account.UnonboardedMemberCleanupBatchSize,
	)
	if unonboardedErr != nil {
		// Scheduled deletions still run; the scheduler logs this failure with the
		// job's result.
		unonboardedErr = fmt.Errorf("dispatch expired unonboarded Member deletions: %w", unonboardedErr)
	}
	if unonboardedQueued > 0 {
		slog.InfoContext(ctx, "Queued expired unonboarded Member hard deletions", "count", unonboardedQueued)
	}
	deletionPublisher, ok := h.publisher.(accountpostgres.UserDeletionDispatchPublisher)
	if !ok {
		return stderrors.Join(unonboardedErr, fmt.Errorf("user deletion command publisher is not configured"))
	}

	// Find deletion requests that have been scheduled and are past the grace period
	var requests []model.UserDeletionRequest
	if err := h.db.WithContext(ctx).
		Where("lifecycle_state IN ?", []string{"scheduled", "recovery_confirmation_pending"}).
		Where("scheduled_at < ?", now).
		Find(&requests).Error; err != nil {
		return stderrors.Join(unonboardedErr, err)
	}

	if len(requests) == 0 {
		return unonboardedErr
	}

	slog.InfoContext(ctx, "Processing scheduled user deletions", "count", len(requests))

	var processedCount int
	for _, req := range requests {
		if err := accountpostgres.DispatchScheduledUserDeletion(ctx, h.db, deletionPublisher, h.spicedbClient, h.memberDeletion, req.ID, now); err != nil {
			if stderrors.Is(err, account.ErrLastActiveAdminDeletion) {
				slog.WarnContext(ctx, "Scheduled member deletion blocked", append(failurecause.Args(err), "member_id", req.MemberID, "identity_id", req.IdentityID, "reason", "last_active_admin")...)
				continue
			}
			slog.ErrorContext(ctx, "Member deletion command publish failed", append(failurecause.Args(err), "member_id", req.MemberID, "identity_id", req.IdentityID)...)
			continue
		}

		processedCount++
		slog.InfoContext(ctx, "Member deletion command accepted", "member_id", req.MemberID, "identity_id", req.IdentityID)
	}

	slog.InfoContext(ctx, "Completed scheduled user deletions",
		"processed", processedCount,
		"total", len(requests))

	return unonboardedErr
}
