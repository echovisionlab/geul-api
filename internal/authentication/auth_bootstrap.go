package authentication

import (
	"context"
	"errors"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

type LoginRoleSyncResult struct {
	Role       policyv1.RoleID
	FirstAdmin bool
}

var errLoginRoleUnchanged = errors.New("login role is unchanged")

// loginRole is the direct global role one login converges on.
type loginRole struct {
	subject    auth.AccountIdentitySubject
	current    policyv1.RoleID
	found      bool
	desired    policyv1.RoleID
	firstAdmin bool
}

// SyncLoginRole performs first-admin bootstrap and role repair
// using account_identity UUIDs as the sole authorization subject. It never
// reads or writes Kratos role metadata and does not use Member as a subject.
//
// The login is the Identity's own lifecycle step, made on no request
// principal's behalf: Access only fences the Identity, and a first-admin
// claim is audited as the backend's.
func (s *Service) SyncLoginRole(
	ctx context.Context,
	identityID string,
	memberID string,
) (LoginRoleSyncResult, error) {
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	if err != nil {
		return LoginRoleSyncResult{}, err
	}
	role := loginRole{subject: subject}
	mutationCtx, cancel := context.WithTimeout(ctx, identitystate.MutationTimeout)
	defer cancel()
	_, err = mutation.Run(mutationCtx, s.mutations, mutation.StoreSpec[loginRole, Store]{
		Access: mutation.PrincipalFence(&auth.UserInfo{IdentityID: auth.IdentityID(identityID)}),
		Change: func(ctx context.Context, store Store) (loginRole, error) {
			if err := s.resolveLoginRole(ctx, store, identityID, memberID, &role); err != nil {
				return role, err
			}
			if role.found && role.desired == role.current && !role.firstAdmin {
				return role, errLoginRoleUnchanged
			}
			return role, nil
		},
		Audit: func(role loginRole) mutation.Audit {
			if !role.firstAdmin {
				return mutation.Unchanged()
			}
			return mutation.SystemAudit(sharedtelemetry.ServiceBackend, sharedtelemetry.AuditMemberUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMemberRoleUpdatedAuditRecord(metadata, memberID, policyv1.Role.User().ID(), policyv1.Role.Admin().ID())
			})
		},
		Relationships: func(role loginRole) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			apply, compensate, err := s.roles.Transition(role.subject, role.desired, role.current, role.found)
			if err != nil {
				return nil, nil, fmt.Errorf("build login direct global role transition: %w", err)
			}
			return apply, compensate, nil
		},
	})
	// The unchanged sentinel rolls the transaction back without a write, and
	// Run returns it unchanged only after a successful rollback. A joined
	// rollback failure must still reach the caller.
	if err == errLoginRoleUnchanged { //nolint:errorlint // identity: a joined rollback failure must not match
		return LoginRoleSyncResult{Role: role.desired, FirstAdmin: role.firstAdmin}, nil
	}
	if err != nil {
		return LoginRoleSyncResult{}, err
	}
	return LoginRoleSyncResult{Role: role.desired, FirstAdmin: role.firstAdmin}, nil
}

// resolveLoginRole reads the current direct role into role and decides the
// desired one: the first-admin claimant becomes an administrator, and a
// subject without a direct role becomes a user.
func (s *Service) resolveLoginRole(ctx context.Context, store Store, identityID, memberID string, role *loginRole) error {
	current, found, err := s.spiceDB.ReadDirectGlobalRole(ctx, role.subject)
	if err != nil {
		return fmt.Errorf("read login direct global role: %w", err)
	}
	role.current, role.found, role.desired = current, found, current
	if !found {
		role.desired = policyv1.Role.User()
	}
	claim, err := store.ClaimFirstAdmin(ctx, identityID, memberID)
	if err != nil {
		return err
	}
	if claim.Owner {
		role.desired = policyv1.Role.Admin()
	}
	role.firstAdmin = claim.ClaimedNow
	return nil
}

// EnsureLoginRole is the handler-facing hard-cut contract. It returns only
// whether this login claimed the first-admin bootstrap; callers must not use a
// role string from authentication as an authorization decision.
func (s *Service) EnsureLoginRole(
	ctx context.Context,
	identityID string,
	memberID string,
) (bool, error) {
	result, err := s.SyncLoginRole(ctx, identityID, memberID)
	if err != nil {
		return false, err
	}
	return result.FirstAdmin, nil
}
