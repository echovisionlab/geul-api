package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/failurecause"
)

// handleProcessUserDeletions scrubs expired email projections, queues the
// expired unonboarded Members' deletions and dispatches each due scheduled
// deletion. A failed unonboarded dispatch or a failed request does not stop
// the scheduled deletions; the job reports the unonboarded failure.
func (h *Handlers) handleProcessUserDeletions(ctx context.Context, now time.Time) error {
	scrubbed, err := h.memberDeletions.ScrubExpiredEmailProjections(ctx, now)
	if err != nil {
		return fmt.Errorf("scrub expired Member email projections: %w", err)
	}
	if scrubbed > 0 {
		slog.InfoContext(ctx, "Scrubbed expired Member email projections", "count", scrubbed)
	}
	unonboardedQueued, unonboardedErr := h.memberDeletions.EnqueueExpiredUnonboarded(ctx, now)
	if unonboardedErr != nil {
		// Scheduled deletions still run; the scheduler logs this failure with the
		// job's result.
		unonboardedErr = fmt.Errorf("dispatch expired unonboarded Member deletions: %w", unonboardedErr)
	}
	if unonboardedQueued > 0 {
		slog.InfoContext(ctx, "Queued expired unonboarded Member hard deletions", "count", unonboardedQueued)
	}

	requests, err := h.memberDeletions.DueScheduledDeletions(ctx, now)
	if err != nil {
		return errors.Join(unonboardedErr, err)
	}
	if len(requests) == 0 {
		return unonboardedErr
	}

	slog.InfoContext(ctx, "Processing scheduled user deletions", "count", len(requests))

	var processedCount int
	for _, req := range requests {
		if err := h.memberDeletions.DispatchScheduledDeletion(ctx, req.ID, now); err != nil {
			if errors.Is(err, account.ErrLastActiveAdminDeletion) {
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
