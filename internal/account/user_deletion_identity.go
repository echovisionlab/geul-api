package account

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identityread"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// processUnonboardedHardDelete is the worker-side terminal path for a
// retention command. Account keeps the identity fence, Kratos deletion, and
// account-subject cleanup; the Member port owns Member authorization and local
// hard-delete state after the account anchor is gone.
func processUnonboardedHardDelete(
	ctx context.Context,
	db *gorm.DB,
	identity auth.IdentityDeleter,
	spicedb *auth.SpiceDBClient,
	members MemberDeletionLifecycle,
	auditWriter domainaudit.Appender,
	command *managev1.UserDeleteIdentityCommand,
) error {
	if db == nil || identity == nil || spicedb == nil || members == nil || auditWriter == nil || command == nil {
		return fmt.Errorf("unonboarded hard deletion dependencies are required")
	}
	if err := validateDeletionIDPair(command.GetMemberId(), command.GetIdentityId()); err != nil {
		return err
	}
	memberID := strings.TrimSpace(command.GetMemberId())
	identityID := strings.TrimSpace(command.GetIdentityId())

	return identitystate.WithMutation(ctx, db, identityID, func(mutationCtx context.Context, connection *gorm.DB) error {
		target, err := members.PrepareUnonboardedHardDelete(mutationCtx, connection, memberID, identityID)
		if err != nil || target.MemberID == "" {
			return err
		}
		if !target.IdentityLinked {
			exists, err := identityread.Exists(mutationCtx, connection, identityID)
			if err != nil {
				return fmt.Errorf("confirm unonboarded identity absence: %w", err)
			}
			if exists {
				return fmt.Errorf("unonboarded hard deletion identity link is missing while identity exists")
			}
		}
		if err := identity.DeleteIdentity(mutationCtx, identityID); err != nil {
			return fmt.Errorf("delete unonboarded Kratos identity: %w", err)
		}
		if err := deleteAccountIdentityAuthorizationAndAnchor(mutationCtx, connection, spicedb, identityID); err != nil {
			return err
		}
		if err := members.RevokeUnonboardedAuthorization(mutationCtx, spicedb, target); err != nil {
			return err
		}
		return connection.Transaction(func(tx *gorm.DB) error {
			return members.FinalizeUnonboardedHardDelete(mutationCtx, tx, target, accountDeletedAudit(auditWriter))
		})
	})
}

func deleteOrConfirmUserIdentity(
	ctx context.Context,
	db *gorm.DB,
	identity auth.IdentityDeleter,
	request MemberDeletionSnapshot,
) error {
	if !request.AlreadyTombstoned && request.IdentityLinked {
		if err := identity.DeleteIdentity(ctx, request.IdentityID); err != nil {
			return fmt.Errorf("delete identity: %w", err)
		}
		return nil
	}
	exists, err := identityread.Exists(ctx, db, request.IdentityID)
	if err != nil {
		return fmt.Errorf("confirm replay identity absence: %w", err)
	}
	if exists {
		return fmt.Errorf("unlinked Member deletion replay cannot target an existing identity")
	}
	return nil
}
