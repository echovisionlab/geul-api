package account

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/failurecause"
)

func (s *AccountEmailChangeLifecycle) Start(ctx context.Context) {
	reconcile := func() error {
		return s.units.Lead(ctx, LeaderEmailChange, s.Reconcile)
	}
	if err := reconcile(); err != nil {
		logAccountEmailChangeError(ctx, "Initial account email change reconciliation failed", "reconcile_initial_failed", err)
	}
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := reconcile(); err != nil {
				logAccountEmailChangeError(ctx, "Account email change reconciliation failed", "reconcile_scan_failed", err)
			}
		}
	}
}

func (s *AccountEmailChangeLifecycle) Reconcile(ctx context.Context) error {
	var upper *EmailChangeRequest
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		upper, err = store.LatestEmailChangeRequest(ctx)
		return err
	}); err != nil || upper == nil {
		return err
	}

	var cursor *EmailChangeRequest
	for {
		var requests []EmailChangeRequest
		if err := s.units.Read(ctx, func(store Store) error {
			var err error
			requests, err = store.EmailChangeRequests(ctx, *upper, cursor, accountEmailChangeBatchSize)
			return err
		}); err != nil {
			return err
		}
		if len(requests) == 0 {
			return nil
		}

		for i := range requests {
			request := &requests[i]
			if err := s.ReconcileRequest(ctx, request.ID); err != nil {
				slog.ErrorContext(ctx,
					"Account email change request reconcile failed",
					"domain", "auth",
					"event", "auth.account_email_change.reconciliation_failed",
					"outcome", "failed",
					"reason", "request_reconcile_failed",
					"error_type", fmt.Sprintf("%T", err),
					"failure_cause", failurecause.Message(err),
					"request_id", request.ID,
					"identity_id", request.IdentityID,
				)
			}
		}

		last := requests[len(requests)-1]
		cursor = &last
		if last.CreatedAt.After(upper.CreatedAt) ||
			(last.CreatedAt.Equal(upper.CreatedAt) && last.ID >= upper.ID) {
			return nil
		}
	}
}

func logAccountEmailChangeError(ctx context.Context, message, reason string, err error) {
	slog.ErrorContext(ctx,
		message,
		"domain", "auth",
		"event", "auth.account_email_change.reconciliation_failed",
		"outcome", "failed",
		"reason", reason,
		"error_type", fmt.Sprintf("%T", err),
		"failure_cause", failurecause.Message(err),
	)
}
