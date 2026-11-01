package account

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/failurecause"
)

// Start restores the external identity state implied by terminal cancellation
// and recovery facts. It does not publish notifications or persist broker
// progress; explicit requests remain the only notification trigger.
func (s *Service) Start(ctx context.Context) {
	reconcile := func() error {
		return s.units.Lead(ctx, LeaderLifecycle, func(leaderCtx context.Context) error {
			return errors.Join(
				s.ReconcileScheduledIdentityStates(leaderCtx),
				s.ReconcileTerminalIdentityStates(leaderCtx),
			)
		})
	}
	if err := reconcile(); err != nil {
		slog.ErrorContext(ctx, "Initial account lifecycle identity reconciliation failed", "failure_cause", failurecause.Message(err))
	}
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := reconcile(); err != nil {
				slog.ErrorContext(ctx, "Account lifecycle identity reconciliation failed", "failure_cause", failurecause.Message(err))
			}
		}
	}
}

// ReconcileScheduledIdentityStates repairs the external deactivation and
// session-revocation consequences of a committed deletion deadline. Starting
// recovery does not cancel or pause that deadline; only confirmed recovery does.
func (s *Service) ReconcileScheduledIdentityStates(ctx context.Context) error {
	var candidates []DeletionRequest
	if err := s.read(ctx, func(store Store) error {
		var err error
		candidates, err = store.ScheduledIdentityDrift(ctx, accountLifecycleReconcileBatchSize)
		return err
	}); err != nil {
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

// ensureScheduledIdentityInactive deactivates the Identity of a pending
// deletion and revokes its sessions, under its Identity fence.
func (s *Service) ensureScheduledIdentityInactive(
	ctx context.Context,
	requestID string,
	identityID string,
) error {
	identityManager := s.identity
	return s.units.Fenced(ctx, identityID, func(mutationCtx context.Context, steps Steps) error {
		current, found, err := deletionRequestStep(mutationCtx, steps, requestID, identityID, accountLifecycleDeletionPendingStates)
		if err != nil || !found {
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
func (s *Service) ReconcileTerminalIdentityStates(ctx context.Context) error {
	var candidates []DeletionRequest
	if err := s.read(ctx, func(store Store) error {
		var err error
		candidates, err = store.TerminalIdentityDrift(ctx, accountLifecycleReconcileBatchSize)
		return err
	}); err != nil {
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

func (s *Service) ensureTerminalIdentityActive(
	ctx context.Context,
	requestID string,
	identityID string,
) error {
	return s.units.Fenced(ctx, identityID, func(mutationCtx context.Context, steps Steps) error {
		current, found, err := deletionRequestStep(mutationCtx, steps, requestID, identityID, []string{
			accountLifecycleStateCancelled,
			accountLifecycleStateRecovered,
		})
		if err != nil || !found {
			return err
		}
		identity, err := s.identity.GetIdentity(mutationCtx, current.IdentityID)
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
		return s.identity.SetIdentityState(mutationCtx, current.IdentityID, auth.KratosStateActive)
	})
}

// deletionRequestStep reads the Identity's request in one of states in a step
// of its fenced flow.
func deletionRequestStep(ctx context.Context, steps Steps, requestID, identityID string, states []string) (DeletionRequest, bool, error) {
	var (
		current DeletionRequest
		found   bool
	)
	err := steps.Step(ctx, func(store Store) error {
		var err error
		current, found, err = store.DeletionRequestInStates(ctx, requestID, identityID, states)
		return err
	})
	return current, found, err
}

func identityMetadataBanned(identity *auth.Identity) bool {
	if identity == nil || identity.MetadataAdmin == nil {
		return false
	}
	banned, _ := identity.MetadataAdmin["banned"].(bool)
	return banned
}
