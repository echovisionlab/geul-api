package account

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/postgreslock"
	"gorm.io/gorm"
)

// Start restores the external identity state implied by terminal cancellation
// and recovery facts. It does not publish notifications or persist broker
// progress; explicit requests remain the only notification trigger.
func (s *AccountLifecycleService) Start(ctx context.Context) {
	reconcile := func() error {
		return postgreslock.WithAdvisoryLeader(
			ctx,
			s.db,
			accountLifecycleLeaderLockKey,
			func(leaderCtx context.Context) error {
				return errors.Join(
					s.ReconcileScheduledIdentityStates(leaderCtx),
					s.ReconcileTerminalIdentityStates(leaderCtx),
				)
			},
		)
	}
	if err := reconcile(); err != nil {
		slog.Error("Initial account lifecycle identity reconciliation failed", "error", err)
	}
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := reconcile(); err != nil {
				slog.Error("Account lifecycle identity reconciliation failed", "error", err)
			}
		}
	}
}

// ReconcileScheduledIdentityStates repairs the external deactivation and
// session-revocation consequences of a committed deletion deadline. Starting
// recovery does not cancel or pause that deadline; only confirmed recovery does.
func (s *AccountLifecycleService) ReconcileScheduledIdentityStates(ctx context.Context) error {
	var candidates []model.UserDeletionRequest
	if err := s.db.WithContext(ctx).Raw(`
		SELECT request.*
		FROM user_deletion_request AS request
		JOIN kratos.identities AS identity ON identity.id = request.identity_id
		WHERE request.lifecycle_state IN ('scheduled', 'recovery_confirmation_pending')
		  AND (
		    identity.state = 'active'
		    OR EXISTS (
		      SELECT 1
		      FROM kratos.sessions AS session
		      WHERE session.identity_id = request.identity_id
		        AND session.active = TRUE
		    )
		  )
		ORDER BY request.updated_at ASC, request.id ASC
		LIMIT ?
	`, accountLifecycleReconcileBatchSize).Scan(&candidates).Error; err != nil {
		return err
	}
	var reconcileErrors []error
	for i := range candidates {
		candidate := candidates[i]
		if err := s.ensureScheduledIdentityInactive(ctx, candidate.ID, candidate.IdentityID); err != nil {
			reconcileErrors = append(reconcileErrors, fmt.Errorf("reconcile scheduled identity %s: %w", candidate.IdentityID, err))
		}
	}
	return errors.Join(reconcileErrors...)
}

func (s *AccountLifecycleService) ensureScheduledIdentityInactive(
	ctx context.Context,
	requestID string,
	identityID string,
) error {
	return ensureScheduledIdentityInactive(ctx, s.db, s.kratosClient, requestID, identityID)
}

func ensureScheduledIdentityInactive(
	ctx context.Context,
	db *gorm.DB,
	identityManager auth.IdentityManager,
	requestID string,
	identityID string,
) error {
	return identitystate.WithMutation(ctx, db, identityID, func(mutationCtx context.Context, tx *gorm.DB) error {
		var current model.UserDeletionRequest
		if err := tx.Where("id = ? AND identity_id = ?::uuid AND lifecycle_state IN ?", requestID, identityID, accountLifecycleDeletionPendingStates).
			First(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		identity, err := identityManager.GetIdentity(mutationCtx, current.IdentityID)
		if err != nil {
			return err
		}
		if identity == nil {
			return fmt.Errorf("identity %s was not found", current.IdentityID)
		}
		if identity.State == auth.KratosStateActive {
			if err := identityManager.SetIdentityState(mutationCtx, current.IdentityID, auth.KratosStateInactive); err != nil {
				return err
			}
		}
		return identityManager.DeleteIdentitySessions(mutationCtx, current.IdentityID)
	})
}

// ReconcileTerminalIdentityStates activates identities whose durable deletion
// lifecycle fact is cancelled or recovered but whose Kratos state is still
// inactive. The cross-schema predicate keeps each pass bounded to actual drift.
func (s *AccountLifecycleService) ReconcileTerminalIdentityStates(ctx context.Context) error {
	var candidates []model.UserDeletionRequest
	if err := s.db.WithContext(ctx).Raw(`
		SELECT request.*
		FROM user_deletion_request AS request
		JOIN kratos.identities AS identity ON identity.id = request.identity_id
		WHERE request.lifecycle_state IN ('cancelled', 'recovered')
		  AND identity.state = 'inactive'
		  AND request.updated_at > identity.updated_at
		ORDER BY request.updated_at ASC, request.id ASC
		LIMIT ?
	`, accountLifecycleReconcileBatchSize).Scan(&candidates).Error; err != nil {
		return err
	}

	var reconcileErrors []error
	for i := range candidates {
		candidate := candidates[i]
		err := s.ensureTerminalIdentityActive(ctx, candidate.ID, candidate.IdentityID)
		if err != nil {
			reconcileErrors = append(reconcileErrors, fmt.Errorf("reconcile identity %s: %w", candidate.IdentityID, err))
		}
	}
	return errors.Join(reconcileErrors...)
}

func (s *AccountLifecycleService) ensureTerminalIdentityActive(
	ctx context.Context,
	requestID string,
	identityID string,
) error {
	return identitystate.WithMutation(ctx, s.db, identityID, func(mutationCtx context.Context, tx *gorm.DB) error {
		var current model.UserDeletionRequest
		if err := tx.Where("id = ? AND identity_id = ?::uuid AND lifecycle_state IN ?", requestID, identityID, []string{
			accountLifecycleStateCancelled,
			accountLifecycleStateRecovered,
		}).
			First(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		identity, err := s.kratosClient.GetIdentity(mutationCtx, current.IdentityID)
		if err != nil {
			return err
		}
		if identity == nil {
			return fmt.Errorf("identity %s was not found", current.IdentityID)
		}
		if identity.State != auth.KratosStateInactive {
			return nil
		}
		if !current.UpdatedAt.After(identity.UpdatedAt) || identityMetadataBanned(identity) {
			return nil
		}
		return s.kratosClient.SetIdentityState(mutationCtx, current.IdentityID, auth.KratosStateActive)
	})
}

func identityMetadataBanned(identity *auth.Identity) bool {
	if identity == nil || identity.MetadataAdmin == nil {
		return false
	}
	banned, _ := identity.MetadataAdmin["banned"].(bool)
	return banned
}
