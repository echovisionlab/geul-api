package entityparticipant

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/memberref"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// Members supplies the Member-owned summaries of participants and the
// Identities they act through.
type Members interface {
	LoadMemberSummaries(context.Context, []string) (map[string]*memberref.Summary, error)
	LoadAuthorizationEligibleMemberSummary(context.Context, string) (*memberref.Summary, error)
	// LoadAuthorizationEligibleIdentities maps each active, onboarded Member
	// whose linked Identity is active and not banned to that Identity.
	LoadAuthorizationEligibleIdentities(context.Context, []string) (map[string]string, error)
}

// View is one participant as the domain RPCs return it.
type View struct {
	Member                *memberref.Summary
	Role                  string
	CreatedAt             time.Time
	HasEffectiveAuthority bool
}

// MapViews projects participant views into the domain's response type.
func MapViews[T any](views []View, project func(View) T) []T {
	result := make([]T, len(views))
	for i := range views {
		result[i] = project(views[i])
	}
	return result
}

// Views projects the entity's participants, as Store.ListParticipants
// lists them, with their Member summaries. HasEffectiveAuthority is true
// only for an active Member that SpiceDB still lets manage the entity.
func (s Spec) Views(ctx context.Context, checker ActorChecker, members Members, entityID string, participants []Participant) ([]View, error) {
	memberIDs := make([]string, 0, len(participants))
	for _, participant := range participants {
		memberIDs = append(memberIDs, participant.MemberID)
	}
	identities, err := members.LoadAuthorizationEligibleIdentities(ctx, memberIDs)
	if err != nil {
		return nil, err
	}
	summaries, err := members.LoadMemberSummaries(ctx, memberIDs)
	if err != nil {
		return nil, err
	}
	manageCan, err := s.Policy.Manage(entityID)
	if err != nil {
		return nil, err
	}
	views := make([]View, 0, len(participants))
	for _, participant := range participants {
		summary := summaries[participant.MemberID]
		if summary == nil {
			return nil, errors.New(s.Title + " participant Member was not found")
		}
		hasEffectiveAuthority, err := hasEffectiveAuthority(ctx, checker, identities[participant.MemberID], manageCan)
		if err != nil {
			return nil, err
		}
		views = append(views, View{
			Member: summary, Role: participant.Role, CreatedAt: participant.CreatedAt,
			HasEffectiveAuthority: hasEffectiveAuthority,
		})
	}
	return views, nil
}

// hasEffectiveAuthority reports whether identityID, the Identity a
// participant may act through (empty when it may not), may manage the
// entity.
func hasEffectiveAuthority(ctx context.Context, checker ActorChecker, identityID string, manageCan policyv1.Can) (bool, error) {
	if identityID == "" {
		return false, nil
	}
	actor, err := policyv1.NewAccountIdentityActor(identityID)
	if err != nil {
		return false, err
	}
	allowed, err := checker.CheckActorCan(ctx, actor, manageCan)
	if err != nil {
		return false, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	return allowed, nil
}

// participantMutation is one participant change inside its authorization
// mutation transaction.
type participantMutation struct {
	spec     Spec
	checker  PermissionChecker
	store    Store
	entityID string
	memberID string
}

// lockPrincipal locks the principal and answers not found once the principal
// is no longer active.
func (m participantMutation) lockPrincipal(ctx context.Context, lockActive func(*auth.UserInfo) (bool, error)) (*auth.UserInfo, error) {
	principal, err := m.spec.LockActivePrincipal(ctx, lockActive)
	if err != nil {
		return nil, err
	}
	if principal == nil {
		return nil, domainerr.ResourceNotFound(m.spec.Name, m.entityID)
	}
	return principal, nil
}

func (m participantMutation) currentRole(ctx context.Context) (string, error) {
	return m.store.ParticipantRole(ctx, m.entityID, m.memberID)
}

// requireManage requires ManageParticipants, or RemoveOwner to demote an
// Owner.
func (m participantMutation) requireManage(ctx context.Context, principal *auth.UserInfo, previous, next string) error {
	action := auth.ResourceAction(m.spec.Policy.ManageParticipants)
	if previous == RoleOwner && next != RoleOwner {
		action = m.spec.Policy.RemoveOwner
	}
	return m.requirePermission(ctx, action, principal)
}

func (m participantMutation) alreadyHasRole() error {
	return domainerr.Precondition(m.spec.Title + " participant already has this role")
}

// requireRemove requires RemoveOwner to remove an Owner and
// ManageParticipants otherwise, and returns the role the Member holds as far
// as the owner table tells.
func (m participantMutation) requireRemove(ctx context.Context, principal *auth.UserInfo) (string, error) {
	ownerCount, err := m.store.CountOwners(ctx, m.entityID, m.memberID)
	if err != nil {
		return RoleNone, err
	}
	if ownerCount > 0 {
		return RoleOwner, m.requirePermission(ctx, m.spec.Policy.RemoveOwner, principal)
	}
	return RoleManager, m.requirePermission(ctx, m.spec.Policy.ManageParticipants, principal)
}

// deleteForRemoval deletes the relation requireRemove found. An Owner must
// leave another Owner; a missing Manager relation answers not found.
func (m participantMutation) deleteForRemoval(ctx context.Context, role string) error {
	if role == RoleOwner {
		if err := m.requireAnotherOwner(ctx); err != nil {
			return err
		}
		_, err := m.store.DeleteParticipant(ctx, m.entityID, m.memberID, RoleOwner)
		return err
	}
	deleted, err := m.store.DeleteParticipant(ctx, m.entityID, m.memberID, RoleManager)
	if err != nil {
		return err
	}
	if !deleted {
		return domainerr.New(domainerr.NotFound, "", m.spec.Title+" participant not found")
	}
	return nil
}

func (m participantMutation) requireAnotherOwner(ctx context.Context) error {
	totalOwners, err := m.store.CountOwners(ctx, m.entityID, "")
	if err != nil {
		return err
	}
	if totalOwners <= 1 {
		return domainerr.Precondition(m.spec.Article + " " + lastOwnerConstraint)
	}
	return nil
}

func (m participantMutation) insertRelation(ctx context.Context, role string) (time.Time, error) {
	createdAt := dbtime.Now()
	return createdAt, m.store.InsertParticipant(ctx, m.entityID, m.memberID, role, createdAt)
}

func (m participantMutation) requirePermission(ctx context.Context, action auth.ResourceAction, principal *auth.UserInfo) error {
	can, err := action(m.entityID)
	if err != nil {
		return err
	}
	decision, err := auth.AuthorizationDecision(auth.WithUser(ctx, principal), can)
	if err != nil {
		return domainerr.ResourceNotFound(m.spec.Name, m.entityID).WithCause(err)
	}
	allowed, err := m.checker.Can(ctx, decision)
	if err != nil {
		return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if !allowed {
		return domainerr.ResourceNotFound(m.spec.Name, m.entityID)
	}
	return nil
}

func auditRelationship(role string) sharedtelemetry.AuditRelationship {
	switch role {
	case RoleOwner:
		return sharedtelemetry.AuditRelationshipOwner
	case RoleManager:
		return sharedtelemetry.AuditRelationshipManager
	default:
		return sharedtelemetry.AuditRelationshipNone
	}
}
