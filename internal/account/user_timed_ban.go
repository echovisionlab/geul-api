package account

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

var ErrPendingAccountDeletion = errors.New("account deletion is pending")

func hasPendingAccountDeletion(tx *gorm.DB, identityID string) (bool, error) {
	var pendingDeletionCount int64
	if err := tx.Model(&model.UserDeletionRequest{}).
		Where("identity_id = ? AND lifecycle_state IN ?", identityID, accountLifecycleDeletionPendingStates).
		Count(&pendingDeletionCount).Error; err != nil {
		return false, fmt.Errorf("check pending account deletion: %w", err)
	}
	return pendingDeletionCount > 0, nil
}

func clearUserBan(
	ctx context.Context,
	db *gorm.DB,
	identityManager auth.IdentityManager,
	identityID string,
	afterClear func(context.Context, *gorm.DB) error,
) (bool, error) {
	cleared := false
	err := identitystate.WithMutation(ctx, db, identityID, func(mutationCtx context.Context, tx *gorm.DB) error {
		pendingDeletion, err := hasPendingAccountDeletion(tx, identityID)
		if err != nil {
			return err
		}
		if pendingDeletion {
			return ErrPendingAccountDeletion
		}
		identity, err := identityManager.GetIdentity(mutationCtx, identityID)
		if err != nil {
			return err
		}
		if identity == nil {
			return errors.New("unban identity was not found")
		}
		if !identity.IsBanned() {
			return nil
		}
		if err := NewUserStateService(identityManager).ClearBan(mutationCtx, identityID); err != nil {
			return err
		}
		if afterClear != nil {
			if err := afterClear(mutationCtx, tx); err != nil {
				return err
			}
		}
		cleared = true
		return nil
	})
	return cleared, err
}

// ClearExpiredTimedBan clears an expired timed ban only after re-reading both
// authorities under the per-user identity-state fence. A pending deletion
// lifecycle remains authoritative for inactivity and must not be undone by a
// stale scheduler snapshot.
func ClearExpiredTimedBan(
	ctx context.Context,
	db *gorm.DB,
	identityManager auth.IdentityManager,
	identityID string,
	now time.Time,
	auditWriters ...domainaudit.Appender,
) (bool, error) {
	if identityManager == nil {
		return false, fmt.Errorf("expired ban reconciliation requires identity manager")
	}
	identityID = strings.TrimSpace(identityID)
	if identityID == "" {
		return false, fmt.Errorf("expired ban reconciliation requires identity id")
	}

	cleared := false
	var auditWriter domainaudit.Appender
	if len(auditWriters) > 0 {
		auditWriter = auditWriters[0]
	}
	err := identitystate.WithMutation(ctx, db, identityID, func(mutationCtx context.Context, tx *gorm.DB) error {
		pendingDeletion, err := hasPendingAccountDeletion(tx, identityID)
		if err != nil {
			return err
		}
		if pendingDeletion {
			return nil
		}

		identity, err := identityManager.GetIdentity(mutationCtx, identityID)
		if err != nil {
			return err
		}
		if identity == nil {
			return errors.New("expired ban identity was not found")
		}
		if !identityHasExpiredTimedBan(identity, now) {
			return nil
		}
		if err := NewUserStateService(identityManager).ClearBan(mutationCtx, identityID); err != nil {
			return err
		}
		if auditWriter != nil {
			memberID, err := authorizationtarget.ActiveMemberIDForIdentity(mutationCtx, tx, identityID)
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.New("expired ban member was not found")
			}
			if err != nil {
				return err
			}
			if err := domainaudit.AppendSystem(
				mutationCtx,
				tx,
				auditWriter,
				sharedtelemetry.ServiceBackend,
				sharedtelemetry.AuditMemberUpdated,
				func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
					return sharedtelemetry.NewMemberUnbannedAuditRecord(metadata, memberID)
				},
			); err != nil {
				return err
			}
		}
		cleared = true
		return nil
	})
	return cleared, err
}

func identityHasExpiredTimedBan(identity *auth.Identity, now time.Time) bool {
	if identity == nil || identity.MetadataAdmin == nil {
		return false
	}
	banned, ok := identity.MetadataAdmin["banned"].(bool)
	if !ok || !banned {
		return false
	}
	expiresRaw, ok := identity.MetadataAdmin["ban_expires"].(string)
	if !ok || strings.TrimSpace(expiresRaw) == "" {
		return false
	}
	expiresAt, err := time.Parse(time.RFC3339, expiresRaw)
	return err == nil && !expiresAt.After(now)
}
