package authentication

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identityread"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

type LoginRoleSyncResult struct {
	Role       policyv1.RoleID
	FirstAdmin bool
}

var errLoginRoleUnchanged = errors.New("login role is unchanged")

// claimFirstAdmin claims the first-admin bootstrap for identityID when the
// greenfield allows it, and reports whether identityID holds the claim and
// whether this call made it.
func claimFirstAdmin(tx *gorm.DB, identityID, memberID string) (owner bool, claimedNow bool, err error) {
	if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "geul:first-admin-bootstrap").Error; err != nil {
		return false, false, err
	}

	// Runtime bootstrap is only valid for an explicitly empty greenfield.
	// Imported/cutover data must carry its approved first-admin result rather
	// than electing an arbitrary earliest Kratos identity at login time.
	insert := tx.Exec(
		`INSERT INTO auth_bootstrap_state (key, identity_id, member_id)
		SELECT ?, ?::uuid, ?::uuid
		WHERE EXISTS (
			SELECT 1
			FROM member
			`+identityread.JoinLinked("member", "identity")+`
			WHERE member.id = ?::uuid
			  AND identity.id = ?::uuid
			  AND member.deleted_at IS NULL
			  AND member.onboarded = TRUE
			  AND NOT `+identityread.BannedSQL("identity")+`
		)
		AND NOT EXISTS (
			SELECT 1 FROM public.account_identity
			WHERE id <> ?::uuid
		)
		ON CONFLICT (key) DO NOTHING`,
		"first_admin", identityID, memberID, memberID, identityID, identityID,
	)
	if insert.Error != nil {
		return false, false, insert.Error
	}
	claimedNow = insert.RowsAffected > 0

	var claimOwnerID string
	if err := tx.Raw(
		`SELECT COALESCE(identity_id::text, '') FROM auth_bootstrap_state WHERE key = ?`,
		"first_admin",
	).Scan(&claimOwnerID).Error; err != nil {
		return false, false, err
	}
	return claimOwnerID == identityID, claimedNow, nil
}

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
	_, err = authzmutation.Execute(mutationCtx, s.db, s.spiceDB, func(
		tx *gorm.DB,
		write authzmutation.WriteRelationships,
	) error {
		if err := identitystate.Lock(tx, identityID); err != nil {
			return err
		}
		if err := s.resolveLoginRole(mutationCtx, tx, identityID, memberID, &role); err != nil {
			return err
		}
		if role.found && role.desired == role.current && !role.firstAdmin {
			return errLoginRoleUnchanged
		}
		return s.replaceLoginRole(mutationCtx, tx, write, memberID, role)
	})
	// Execute returns the sentinel unchanged only after a successful rollback.
	// A joined rollback failure must still reach the caller.
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
func (s *Service) resolveLoginRole(ctx context.Context, tx *gorm.DB, identityID, memberID string, role *loginRole) error {
	current, found, err := s.spiceDB.ReadDirectGlobalRole(ctx, role.subject)
	if err != nil {
		return fmt.Errorf("read login direct global role: %w", err)
	}
	role.current, role.found, role.desired = current, found, current
	if !found {
		role.desired = policyv1.Role.User()
	}
	owner, claimedNow, err := claimFirstAdmin(tx, identityID, memberID)
	if err != nil {
		return err
	}
	if owner {
		role.desired = policyv1.Role.Admin()
	}
	role.firstAdmin = claimedNow
	return nil
}

// replaceLoginRole writes the desired direct role and audits a first-admin
// claim as the backend's.
func (s *Service) replaceLoginRole(ctx context.Context, tx *gorm.DB, write authzmutation.WriteRelationships, memberID string, role loginRole) error {
	apply, compensate, err := s.roles.Transition(role.subject, role.desired, role.current, role.found)
	if err != nil {
		return fmt.Errorf("build login direct global role transition: %w", err)
	}
	if err := write(apply, compensate); err != nil {
		return fmt.Errorf("replace login direct global role: %w", err)
	}
	if !role.firstAdmin {
		return nil
	}
	return domainaudit.AppendSystem(
		ctx,
		tx,
		s.audit,
		sharedtelemetry.ServiceBackend,
		sharedtelemetry.AuditMemberUpdated,
		func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewMemberRoleUpdatedAuditRecord(metadata, memberID, policyv1.Role.User().ID(), policyv1.Role.Admin().ID())
		},
	)
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
