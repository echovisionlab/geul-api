package account

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

var ErrPendingAccountDeletion = errors.New("account deletion is pending")

// clearUserBan lifts the ban of identityID in the store's transaction, which
// holds its Identity fence, and reports whether it was banned. A pending
// deletion keeps the Identity inactive.
func clearUserBan(ctx context.Context, store Store, identityManager userStateIdentities, identityID string) (bool, error) {
	pendingDeletion, err := store.HasPendingDeletion(ctx, identityID)
	if err != nil {
		return false, err
	}
	if pendingDeletion {
		return false, ErrPendingAccountDeletion
	}
	identity, err := identityManager.GetIdentity(ctx, identityID)
	if err != nil {
		return false, err
	}
	if identity == nil {
		return false, errors.New("unban identity was not found")
	}
	if !identity.IsBanned() {
		return false, nil
	}
	return true, NewUserStateService(identityManager).ClearBan(ctx, identityID)
}

// ClearExpiredTimedBan clears an expired timed ban only after re-reading both
// authorities under the per-user identity-state fence. A pending deletion
// lifecycle remains authoritative for inactivity and must not be undone by a
// stale scheduler snapshot. The Kratos write commits before the audit step,
// which records the cleared ban attributed to the backend.
func ClearExpiredTimedBan(
	ctx context.Context,
	units Units,
	identityManager userStateIdentities,
	identityID string,
	now time.Time,
	auditWriter domainaudit.Appender,
) (bool, error) {
	if identityManager == nil {
		return false, fmt.Errorf("expired ban reconciliation requires identity manager")
	}
	identityID = strings.TrimSpace(identityID)
	if identityID == "" {
		return false, fmt.Errorf("expired ban reconciliation requires identity id")
	}

	cleared := false
	err := units.Fenced(ctx, identityID, func(mutationCtx context.Context, steps Steps) error {
		var pendingDeletion bool
		if err := steps.Step(mutationCtx, func(store Store) error {
			var err error
			pendingDeletion, err = store.HasPendingDeletion(mutationCtx, identityID)
			return err
		}); err != nil || pendingDeletion {
			return err
		}

		identity, err := identityManager.GetIdentity(mutationCtx, identityID)
		if err != nil {
			return err
		}
		if identity == nil {
			return errors.New("expired ban identity was not found")
		}
		expired, err := identityHasExpiredTimedBan(identity, now)
		if err != nil || !expired {
			return err
		}
		if err := NewUserStateService(identityManager).ClearBan(mutationCtx, identityID); err != nil {
			return err
		}
		var memberID string
		if err := steps.Step(mutationCtx, func(store Store) error {
			var err error
			memberID, err = store.ActiveMemberIDForIdentity(mutationCtx, identityID)
			return err
		}); err != nil {
			if errors.Is(err, ErrMemberLinkNotFound) {
				return errors.New("expired ban member was not found")
			}
			return err
		}
		if err := steps.Step(mutationCtx, func(store Store) error {
			return store.AppendSystemAudit(mutationCtx, auditWriter, sharedtelemetry.AuditMemberUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMemberUnbannedAuditRecord(metadata, memberID)
			})
		}); err != nil {
			return err
		}
		cleared = true
		return nil
	})
	return cleared, err
}

// identityHasExpiredTimedBan reports an unreadable expiry as an error, so
// the unban scheduler logs it instead of skipping the identity silently.
func identityHasExpiredTimedBan(identity *auth.Identity, now time.Time) (bool, error) {
	if identity == nil || identity.MetadataAdmin == nil {
		return false, nil
	}
	banned, ok := identity.MetadataAdmin["banned"].(bool)
	if !ok || !banned {
		return false, nil
	}
	expiresRaw, ok := identity.MetadataAdmin["ban_expires"].(string)
	if !ok || strings.TrimSpace(expiresRaw) == "" {
		return false, nil
	}
	expiresAt, err := time.Parse(time.RFC3339, expiresRaw)
	if err != nil {
		return false, failurecause.WithPrivate(fmt.Errorf("parse timed ban expiry: %w", err), expiresRaw)
	}
	return !expiresAt.After(now), nil
}
