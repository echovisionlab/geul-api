package account

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

func (s *AccountService) accountSummaryForAdmin(
	ctx context.Context,
	memberID string,
) (*connect.Response[managev1.AccountSummary], error) {
	if _, err := authorizationtarget.RequireGlobalAdmin(ctx, s.spicedb); err != nil {
		return nil, err
	}
	summary, err := accountSummaryForMember(ctx, s.db, s.spicedb, memberID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(summary), nil
}

func (s *AccountService) SetAccountRole(
	ctx context.Context,
	req *connect.Request[managev1.SetAccountRoleRequest],
) (*connect.Response[managev1.AccountSummary], error) {
	actor, err := authorizationtarget.RequireGlobalAdmin(ctx, s.spicedb)
	if err != nil {
		return nil, err
	}
	if req.Msg.MemberId == actor.MemberID.String() {
		return nil, errs.InvalidArgument("member_id", "cannot change your own role")
	}
	role, err := accountRoleForProto(req.Msg.Role)
	if err != nil {
		return nil, err
	}
	target, err := authorizationtarget.RequireLive(ctx, s.db, req.Msg.MemberId)
	if err != nil {
		return nil, err
	}
	err = s.setAccountRoleWithIdentityMutation(ctx, target, role)
	if err != nil {
		var connectErr *connect.Error
		if errors.As(err, &connectErr) {
			return nil, connectErr
		}
		return nil, errs.Internal(err)
	}
	return s.accountSummaryForAdmin(ctx, req.Msg.MemberId)
}

var errAccountRoleUnchanged = errors.New("account role is unchanged")

func (s *AccountService) setAccountRoleWithIdentityMutation(
	ctx context.Context,
	target authorizationtarget.Target,
	role policyv1.RoleID,
) error {
	mutationCtx, cancel := context.WithTimeout(ctx, identitystate.MutationTimeout)
	defer cancel()
	_, err := authzmutation.Execute(mutationCtx, s.db, s.spicedb, func(
		tx *gorm.DB,
		write authzmutation.WriteRelationships,
	) error {
		actor, err := authorizationtarget.RequireGlobalAdmin(mutationCtx, s.spicedb)
		if err != nil {
			return err
		}
		if err := lockAccountRoleIdentityFences(tx, actor.IdentityID.String(), target.IdentityID); err != nil {
			return err
		}
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", userDeletionAdminFenceKey).Error; err != nil {
			return err
		}
		currentActor, err := authorizationtarget.RequireLive(mutationCtx, tx, actor.MemberID.String())
		if err != nil {
			return err
		}
		if currentActor.IdentityID != actor.IdentityID.String() {
			return errs.InvalidSession()
		}
		adminCan, err := policyv1.Platform.IsAdmin()
		if err != nil {
			return err
		}
		decision, err := auth.AuthorizationDecision(mutationCtx, adminCan)
		if err != nil {
			return errs.InvalidSession()
		}
		isAdmin, err := s.spicedb.Can(mutationCtx, decision)
		if err != nil {
			return fmt.Errorf("check current global admin role: %w", err)
		}
		if !isAdmin {
			return errs.AdminRequired()
		}
		currentTarget, err := authorizationtarget.RequireLive(mutationCtx, tx, target.MemberID)
		if err != nil {
			return err
		}
		if currentTarget.IdentityID != target.IdentityID {
			return errs.NotFound("member", target.MemberID)
		}
		targetSubject, err := auth.NewAccountIdentitySubject(auth.IdentityID(target.IdentityID))
		if err != nil {
			return err
		}
		previousRole, previousFound, err := s.spicedb.ReadDirectGlobalRole(mutationCtx, targetSubject)
		if err != nil {
			return fmt.Errorf("read current direct global role: %w", err)
		}
		if previousFound && previousRole == role {
			return errAccountRoleUnchanged
		}
		if role != policyv1.Role.Admin() {
			if err := ValidateLastActiveAdminDeletionWithAuthorization(
				mutationCtx,
				tx,
				target.MemberID,
				target.IdentityID,
				s.spicedb,
			); err != nil {
				return err
			}
		}
		if s.auditWriter == nil {
			return fmt.Errorf("account role audit writer is required")
		}
		apply, err := accountRoleReplacementMutations(targetSubject, role)
		if err != nil {
			return fmt.Errorf("build direct global role replacement: %w", err)
		}
		compensate, err := accountRoleRestoreMutations(targetSubject, previousRole, previousFound)
		if err != nil {
			return fmt.Errorf("build inverse direct global role replacement: %w", err)
		}
		if err := write(apply, compensate); err != nil {
			return fmt.Errorf("replace direct global role: %w", err)
		}
		if err := domainaudit.AppendRequest(
			mutationCtx,
			tx,
			s.auditWriter,
			sharedtelemetry.AuditMemberUpdated,
			func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMemberRoleUpdatedAuditRecord(
					metadata, target.MemberID, previousRole.ID(), role.ID(),
				)
			},
		); err != nil {
			return err
		}
		return nil
	})
	if errors.Is(err, errAccountRoleUnchanged) {
		return nil
	}
	if err != nil {
		return err
	}
	return nil
}

// lockAccountRoleIdentityFences holds both lifecycle transaction fences in
// canonical identity order so opposing role changes cannot deadlock.
func lockAccountRoleIdentityFences(
	tx *gorm.DB,
	actorIdentityID string,
	targetIdentityID string,
) error {
	if tx == nil {
		return fmt.Errorf("account role transaction is required")
	}
	identityIDs := []string{strings.TrimSpace(actorIdentityID), strings.TrimSpace(targetIdentityID)}
	if identityIDs[0] == "" || identityIDs[1] == "" {
		return fmt.Errorf("account role identity mutation requires identity ids")
	}
	sort.Strings(identityIDs)
	for index, identityID := range identityIDs {
		if index > 0 && identityID == identityIDs[index-1] {
			continue
		}
		if err := identitystate.Lock(tx, identityID); err != nil {
			return err
		}
	}
	return nil
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
