package account

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/postgreslock"
	"gorm.io/gorm"
)

func (s *AccountEmailChangeLifecycle) Start(ctx context.Context) {
	reconcile := func() error {
		return postgreslock.WithAdvisoryLeader(
			ctx,
			s.db,
			accountEmailChangeLeaderLockKey,
			s.Reconcile,
		)
	}
	if err := reconcile(); err != nil {
		logAccountEmailChangeError("Initial account email change reconciliation failed", "reconcile_initial_failed", err)
	}
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := reconcile(); err != nil {
				logAccountEmailChangeError("Account email change reconciliation failed", "reconcile_scan_failed", err)
			}
		}
	}
}

func (s *AccountEmailChangeLifecycle) Reconcile(ctx context.Context) error {
	var upper model.AccountEmailChangeRequest
	result := s.db.WithContext(ctx).
		Order("created_at DESC, id DESC").
		First(&upper)
	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil
		}
		return result.Error
	}

	var cursorCreatedAt time.Time
	var cursorID string
	for {
		var requests []model.AccountEmailChangeRequest
		query := s.db.WithContext(ctx).
			Where(`
				(created_at < ? OR (created_at = ? AND id <= ?::uuid))
			`, upper.CreatedAt, upper.CreatedAt, upper.ID)
		if !cursorCreatedAt.IsZero() {
			query = query.Where(`
				(created_at > ? OR (created_at = ? AND id > ?::uuid))
			`, cursorCreatedAt, cursorCreatedAt, cursorID)
		}
		if err := query.
			Order("created_at ASC, id ASC").
			Limit(accountEmailChangeBatchSize).
			Find(&requests).Error; err != nil {
			return err
		}
		if len(requests) == 0 {
			return nil
		}

		for i := range requests {
			request := &requests[i]
			if err := s.ReconcileRequest(ctx, request.ID); err != nil {
				slog.Error(
					"Account email change request reconcile failed",
					"domain", "auth",
					"event", "auth.account_email_change.reconciliation_failed",
					"outcome", "failed",
					"reason", "request_reconcile_failed",
					"error_type", fmt.Sprintf("%T", err),
					"request_id", request.ID,
					"identity_id", request.IdentityID,
				)
			}
		}

		last := requests[len(requests)-1]
		cursorCreatedAt = last.CreatedAt
		cursorID = last.ID
		if cursorCreatedAt.After(upper.CreatedAt) ||
			(cursorCreatedAt.Equal(upper.CreatedAt) && cursorID >= upper.ID) {
			return nil
		}
	}
}

func logAccountEmailChangeError(message, reason string, err error) {
	slog.Error(
		message,
		"domain", "auth",
		"event", "auth.account_email_change.reconciliation_failed",
		"outcome", "failed",
		"reason", reason,
		"error_type", fmt.Sprintf("%T", err),
	)
}
