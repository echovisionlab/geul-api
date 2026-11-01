package account

import (
	"context"
	"errors"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

// adminAccountSummary is the Account summary an administrator mutation
// answers with.
func (s *Service) adminAccountSummary(ctx context.Context, memberID string) (*managev1.AccountSummary, error) {
	if _, err := authorizationtarget.RequireGlobalAdmin(ctx, s.spicedb); err != nil {
		return nil, err
	}
	return accountSummaryForMember(ctx, s.db, s.spicedb, memberID)
}

// SetAccountRoleCommand replaces a Member's direct global role. A zero Role
// is not a valid account role.
type SetAccountRoleCommand struct {
	MemberID string
	Role     policyv1.RoleID
}

// SetAccountRole replaces the direct global role of another live Member.
func (s *Service) SetAccountRole(ctx context.Context, command SetAccountRoleCommand) (*managev1.AccountSummary, error) {
	actor, err := authorizationtarget.RequireGlobalAdmin(ctx, s.spicedb)
	if err != nil {
		return nil, err
	}
	if command.MemberID == actor.MemberID.String() {
		return nil, domainerr.InvalidField("member_id", "cannot change your own role")
	}
	if !command.Role.Valid() {
		return nil, domainerr.InvalidField("role", "invalid account role")
	}
	target, err := authorizationtarget.RequireLive(ctx, s.db, command.MemberID)
	if err != nil {
		return nil, err
	}
	if err := s.replaceAccountRole(ctx, actor, target, command.Role); err != nil {
		return nil, err
	}
	return s.adminAccountSummary(ctx, command.MemberID)
}

var errAccountRoleUnchanged = errors.New("account role is unchanged")

// accountRoleChange is the direct global role a replacement found.
type accountRoleChange struct {
	subject       auth.AccountIdentitySubject
	previous      policyv1.RoleID
	previousFound bool
}

// replaceAccountRole writes the target's direct global role in the
// authorization batch of one mutation. Both Identities are locked before the
// last-admin fence, the order every fence holder takes, and the fence is held
// before authorization so a demotion and a deletion of the remaining admins
// cannot both pass.
func (s *Service) replaceAccountRole(ctx context.Context, actor *auth.UserInfo, target authorizationtarget.Target, role policyv1.RoleID) error {
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[accountRoleChange]{
		Subjects: []string{actor.IdentityID.String(), target.IdentityID},
		Lock: func(_ context.Context, tx *gorm.DB) error {
			return tx.Exec("SELECT pg_advisory_xact_lock(?)", userDeletionAdminFenceKey).Error
		},
		Access: s.liveAdmin(actor),
		Change: func(ctx context.Context, tx *gorm.DB) (accountRoleChange, error) {
			return s.prepareAccountRole(ctx, tx, target, role)
		},
		Audit: func(change accountRoleChange) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditMemberUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMemberRoleUpdatedAuditRecord(metadata, target.MemberID, change.previous.ID(), role.ID())
			})
		},
		Relationships: func(change accountRoleChange) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			apply, err := accountRoleReplacementMutations(change.subject, role)
			if err != nil {
				return nil, nil, fmt.Errorf("build direct global role replacement: %w", err)
			}
			compensate, err := accountRoleRestoreMutations(change.subject, change.previous, change.previousFound)
			if err != nil {
				return nil, nil, fmt.Errorf("build inverse direct global role replacement: %w", err)
			}
			return apply, compensate, nil
		},
	})
	// Run returns the sentinel unchanged only after a successful rollback.
	// A joined rollback failure must still reach the caller.
	if err == errAccountRoleUnchanged { //nolint:errorlint // identity: a joined rollback failure must not match
		return nil
	}
	return err
}

// liveAdmin requires the actor to still be a live Member under the same
// Identity and to hold the global admin role, read with full consistency. A
// retired actor answers NotFound, as a retired target does, which
// FreshAdminCan's fence cannot express.
func (s *Service) liveAdmin(actor *auth.UserInfo) mutation.Access {
	return mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
		current, err := authorizationtarget.RequireLive(ctx, tx, actor.MemberID.String())
		if err != nil {
			return err
		}
		if current.IdentityID != actor.IdentityID.String() {
			return domainerr.InvalidSession()
		}
		adminCan, err := policyv1.Platform.IsAdmin()
		if err != nil {
			return err
		}
		decision, err := auth.AuthorizationDecision(ctx, adminCan)
		if err != nil {
			return domainerr.InvalidSession().WithCause(err)
		}
		isAdmin, err := s.spicedb.Can(ctx, decision)
		if err != nil {
			return fmt.Errorf("check current global admin role: %w", err)
		}
		if !isAdmin {
			return domainerr.AdminRequired()
		}
		return nil
	})
}

// prepareAccountRole re-reads the target under its Identity lock and reads
// the role it replaces. An unchanged role rolls the mutation back.
func (s *Service) prepareAccountRole(ctx context.Context, tx *gorm.DB, target authorizationtarget.Target, role policyv1.RoleID) (accountRoleChange, error) {
	current, err := authorizationtarget.RequireLive(ctx, tx, target.MemberID)
	if err != nil {
		return accountRoleChange{}, err
	}
	if current.IdentityID != target.IdentityID {
		return accountRoleChange{}, domainerr.ResourceNotFound("member", target.MemberID)
	}
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(target.IdentityID))
	if err != nil {
		return accountRoleChange{}, err
	}
	previous, previousFound, err := s.spicedb.ReadDirectGlobalRole(ctx, subject)
	if err != nil {
		return accountRoleChange{}, fmt.Errorf("read current direct global role: %w", err)
	}
	if previousFound && previous == role {
		return accountRoleChange{}, errAccountRoleUnchanged
	}
	if role != policyv1.Role.Admin() {
		if err := ValidateLastActiveAdminDeletionWithAuthorization(ctx, tx, target.MemberID, target.IdentityID, s.spicedb); err != nil {
			return accountRoleChange{}, err
		}
	}
	return accountRoleChange{subject: subject, previous: previous, previousFound: previousFound}, nil
}

func globalRoleForActor(
	ctx context.Context,
	authority *auth.SpiceDBClient,
	actor policyv1.Actor,
) (policyv1.RoleID, error) {
	for _, candidate := range []struct {
		can  func() (policyv1.Can, error)
		role policyv1.RoleID
	}{
		{policyv1.Platform.IsAdmin, policyv1.Role.Admin()},
		{policyv1.Platform.IsAuthor, policyv1.Role.Author()},
		{policyv1.Platform.IsUser, policyv1.Role.User()},
	} {
		can, err := candidate.can()
		if err != nil {
			return policyv1.RoleID{}, fmt.Errorf("build global %s role check: %w", candidate.role.ID(), err)
		}
		allowed, err := authority.CheckActorCan(ctx, actor, can)
		if err != nil {
			return policyv1.RoleID{}, fmt.Errorf("check global %s role: %w", candidate.role.ID(), err)
		}
		if allowed {
			return candidate.role, nil
		}
	}
	return policyv1.RoleID{}, fmt.Errorf("account identity has no global SpiceDB role")
}

func globalRolesForAccountIdentities(
	ctx context.Context,
	spicedb *auth.SpiceDBClient,
) (map[string]policyv1.RoleID, error) {
	if spicedb == nil {
		return nil, fmt.Errorf("SpiceDB is required")
	}
	roles := make(map[string]policyv1.RoleID)
	for _, candidate := range []struct {
		lookup policyv1.SubjectLookup
		role   policyv1.RoleID
	}{
		{policyv1.Platform.LookupUserSubjects(), policyv1.Role.User()},
		{policyv1.Platform.LookupAuthorSubjects(), policyv1.Role.Author()},
		{policyv1.Platform.LookupAdminSubjects(), policyv1.Role.Admin()},
	} {
		subjects, err := spicedb.LookupGlobalSubjects(ctx, candidate.lookup)
		if err != nil {
			return nil, fmt.Errorf("lookup global %s subjects: %w", candidate.role.ID(), err)
		}
		for _, subject := range subjects {
			roles[subject.ID.String()] = candidate.role
		}
	}
	return roles, nil
}
