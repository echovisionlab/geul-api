package authentication

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

type AuthBootstrapService struct {
	db          *gorm.DB
	spicedb     *auth.SpiceDBClient
	auditWriter domainaudit.Appender
	roles       DirectRoleTransition
}

// NewAuthBootstrapService constructs the hard-cut login role bootstrap
// service. Kratos remains AuthN; SpiceDB is the sole role authority.
func NewAuthBootstrapService(
	db *gorm.DB,
	spicedb *auth.SpiceDBClient,
	auditWriter domainaudit.Appender,
	roles DirectRoleTransition,
) *AuthBootstrapService {
	if db == nil {
		panic("db is required")
	}
	if spicedb == nil {
		panic("SpiceDB role authority is required")
	}
	if auditWriter == nil {
		panic("auth bootstrap audit writer is required")
	}
	if roles == nil {
		panic("direct role transition is required")
	}
	return &AuthBootstrapService{
		db: db, spicedb: spicedb, auditWriter: auditWriter, roles: roles,
	}
}

type LoginRoleSyncResult struct {
	Role       policyv1.RoleID
	FirstAdmin bool
}

var errLoginRoleUnchanged = errors.New("login role is unchanged")

func (s *AuthBootstrapService) resolveBootstrapRole(
	tx *gorm.DB,
	identityID string,
	memberID string,
	desiredRole policyv1.RoleID,
) (policyv1.RoleID, bool, bool, error) {
	if tx == nil {
		return policyv1.RoleID{}, false, false, gorm.ErrInvalidDB
	}
	if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "geul:first-admin-bootstrap").Error; err != nil {
		return policyv1.RoleID{}, false, false, err
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
			JOIN kratos.identities AS identity
			  ON identity.id = member.account_identity_id
			 AND identity.external_id = member.id::text
			WHERE member.id = ?::uuid
			  AND identity.id = ?::uuid
			  AND member.deleted_at IS NULL
			  AND member.onboarded = TRUE
			  AND identity.state = 'active'
			  AND NOT COALESCE((identity.metadata_admin->>'banned')::boolean, false)
		)
		AND NOT EXISTS (
			SELECT 1 FROM public.account_identity
			WHERE id <> ?::uuid
		)
		ON CONFLICT (key) DO NOTHING`,
		"first_admin", identityID, memberID, memberID, identityID, identityID,
	)
	if insert.Error != nil {
		return policyv1.RoleID{}, false, false, insert.Error
	}
	claimedNow := insert.RowsAffected > 0

	var claimOwnerID string
	if err := tx.Raw(
		`SELECT COALESCE(identity_id::text, '') FROM auth_bootstrap_state WHERE key = ?`,
		"first_admin",
	).Scan(&claimOwnerID).Error; err != nil {
		return policyv1.RoleID{}, false, false, err
	}
	if claimOwnerID == identityID {
		return policyv1.Role.Admin(), claimedNow, true, nil
	}
	return desiredRole, claimedNow, false, nil
}

// SyncLoginRole performs first-admin bootstrap and role repair
// using account_identity UUIDs as the sole authorization subject. It never
// reads or writes Kratos role metadata and does not use Member as a subject.
func (s *AuthBootstrapService) SyncLoginRole(
	ctx context.Context,
	identityID string,
	memberID string,
) (LoginRoleSyncResult, error) {
	if s == nil || s.db == nil || s.spicedb == nil {
		return LoginRoleSyncResult{}, fmt.Errorf("SpiceDB login role authority is required")
	}
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	if err != nil {
		return LoginRoleSyncResult{}, err
	}
	var desiredRole policyv1.RoleID
	var firstAdmin bool
	mutationCtx, cancel := context.WithTimeout(ctx, identitystate.MutationTimeout)
	defer cancel()
	_, err = authzmutation.Execute(mutationCtx, s.db, s.spicedb, func(
		tx *gorm.DB,
		write authzmutation.WriteRelationships,
	) error {
		if err := identitystate.Lock(tx, identityID); err != nil {
			return err
		}
		currentRole, found, err := s.spicedb.ReadDirectGlobalRole(mutationCtx, subject)
		if err != nil {
			return fmt.Errorf("read login direct global role: %w", err)
		}
		desiredRole = currentRole
		if !found {
			desiredRole = policyv1.Role.User()
		}
		resolvedRole, claimedNow, _, err := s.resolveBootstrapRole(tx, identityID, memberID, desiredRole)
		if err != nil {
			return err
		}
		desiredRole = resolvedRole
		firstAdmin = claimedNow
		if found && desiredRole == currentRole && !claimedNow {
			return errLoginRoleUnchanged
		}
		apply, compensate, err := s.roles.Transition(subject, desiredRole, currentRole, found)
		if err != nil {
			return fmt.Errorf("build login direct global role transition: %w", err)
		}
		if err := write(apply, compensate); err != nil {
			return fmt.Errorf("replace login direct global role: %w", err)
		}
		if claimedNow {
			if err := domainaudit.AppendSystem(
				mutationCtx,
				tx,
				s.auditWriter,
				sharedtelemetry.ServiceBackend,
				sharedtelemetry.AuditMemberUpdated,
				func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
					return sharedtelemetry.NewMemberRoleUpdatedAuditRecord(metadata, memberID, policyv1.Role.User().ID(), policyv1.Role.Admin().ID())
				},
			); err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, errLoginRoleUnchanged) {
		return LoginRoleSyncResult{Role: desiredRole, FirstAdmin: firstAdmin}, nil
	}
	if err != nil {
		return LoginRoleSyncResult{}, err
	}
	return LoginRoleSyncResult{Role: desiredRole, FirstAdmin: firstAdmin}, nil
}

// EnsureLoginRole is the handler-facing hard-cut contract. It returns only
// whether this login claimed the first-admin bootstrap; callers must not use a
// role string from authentication as an authorization decision.
func (s *AuthBootstrapService) EnsureLoginRole(
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
