package account

import (
	"context"
	"fmt"
	"strings"
)

// processUnonboardedHardDelete is the worker-side terminal path for a
// retention command. Account keeps the identity fence, Kratos deletion, and
// account-subject cleanup; the Member port owns Member authorization and local
// hard-delete state after the account anchor is gone.
func (d UserDeletion) processUnonboardedHardDelete(ctx context.Context, command DeleteIdentityCommand) error {
	if err := validateDeletionIDPair(command.MemberID, command.IdentityID); err != nil {
		return err
	}
	memberID := strings.TrimSpace(command.MemberID)
	identityID := strings.TrimSpace(command.IdentityID)

	return d.Units.Fenced(ctx, identityID, func(mutationCtx context.Context, steps Steps) error {
		var target MemberUnonboardedDeletionTarget
		if err := steps.Step(mutationCtx, func(store Store) error {
			var err error
			target, err = store.PrepareUnonboardedHardDelete(mutationCtx, memberID, identityID)
			return err
		}); err != nil || target.MemberID == "" {
			return err
		}
		if !target.IdentityLinked {
			exists, err := identityExists(mutationCtx, steps, identityID)
			if err != nil {
				return fmt.Errorf("confirm unonboarded identity absence: %w", err)
			}
			if exists {
				return fmt.Errorf("unonboarded hard deletion identity link is missing while identity exists")
			}
		}
		if err := d.Identities.DeleteIdentity(mutationCtx, identityID); err != nil {
			return fmt.Errorf("delete unonboarded Kratos identity: %w", err)
		}
		if err := d.deleteAccountIdentityAuthorizationAndAnchor(mutationCtx, steps, identityID); err != nil {
			return err
		}
		if err := d.Members.RevokeUnonboardedAuthorization(mutationCtx, target); err != nil {
			return err
		}
		return steps.Step(mutationCtx, func(store Store) error {
			return store.FinalizeUnonboardedHardDelete(mutationCtx, target, accountDeletedAudit(store, d.Audit))
		})
	})
}

func identityExists(ctx context.Context, steps Steps, identityID string) (bool, error) {
	var exists bool
	err := steps.Step(ctx, func(store Store) error {
		var err error
		exists, err = store.IdentityExists(ctx, identityID)
		return err
	})
	return exists, err
}

func (d UserDeletion) deleteOrConfirmUserIdentity(ctx context.Context, steps Steps, request MemberDeletionSnapshot) error {
	if !request.AlreadyTombstoned && request.IdentityLinked {
		if err := d.Identities.DeleteIdentity(ctx, request.IdentityID); err != nil {
			return fmt.Errorf("delete identity: %w", err)
		}
		return nil
	}
	exists, err := identityExists(ctx, steps, request.IdentityID)
	if err != nil {
		return fmt.Errorf("confirm replay identity absence: %w", err)
	}
	if exists {
		return fmt.Errorf("unlinked Member deletion replay cannot target an existing identity")
	}
	return nil
}
