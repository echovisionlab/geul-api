package entityparticipant

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// Mutations runs the entity's participant and metadata mutations through the
// mutation pipeline of the domain store S, which embeds Store.
type Mutations[S Store] struct {
	spec    Spec
	runner  *mutation.StoreRunner[S]
	checker PermissionChecker
}

// NewMutations returns the entity's pipeline mutations.
func NewMutations[S Store](spec Spec, runner *mutation.StoreRunner[S], checker PermissionChecker) Mutations[S] {
	return Mutations[S]{spec: spec, runner: runner, checker: checker}
}

// ParticipantAudit names the audit record of a participant role change.
type ParticipantAudit func(previous, next sharedtelemetry.AuditRelationship) mutation.Audit

// ParticipantChange gives the Member Role on the entity, or removes the
// Member when Role is RoleNone.
type ParticipantChange struct {
	EntityID string
	MemberID string
	Role     string
	Audit    ParticipantAudit
}

// participantResult is a committed participant change.
type participantResult struct {
	previous   string
	identityID string
	createdAt  time.Time
}

// SetParticipant gives the Member role on the entity, moving an existing
// participant between Owner and Manager. Demoting an Owner requires
// RemoveOwner, and the entity always keeps one Owner.
func (m Mutations[S]) SetParticipant(ctx context.Context, members Members, change ParticipantChange) (View, error) {
	var participant participantMutation
	var previous string
	result, err := m.run(ctx, change, &participant, func(ctx context.Context, principal *auth.UserInfo) error {
		var err error
		if previous, err = participant.currentRole(ctx); err != nil {
			return err
		}
		return participant.requireManage(ctx, principal, previous, change.Role)
	}, func(ctx context.Context, store S) (participantResult, error) {
		if previous != RoleNone && previous == change.Role {
			return participantResult{}, participant.alreadyHasRole()
		}
		identityID, err := store.LockMemberIdentity(ctx, change.MemberID)
		if err != nil {
			return participantResult{}, err
		}
		if previous == RoleOwner {
			if err := participant.requireAnotherOwner(ctx); err != nil {
				return participantResult{}, err
			}
		}
		if previous != RoleNone {
			if _, err := store.DeleteParticipant(ctx, change.EntityID, change.MemberID, previous); err != nil {
				return participantResult{}, err
			}
		}
		createdAt, err := participant.insertRelation(ctx, change.Role)
		return participantResult{previous: previous, identityID: identityID, createdAt: createdAt}, err
	})
	if err != nil {
		return View{}, err
	}
	summary, err := members.LoadAuthorizationEligibleMemberSummary(ctx, change.MemberID)
	if err != nil {
		return View{}, err
	}
	return View{Member: summary, Role: change.Role, CreatedAt: result.createdAt, HasEffectiveAuthority: true}, nil
}

// RemoveParticipant removes the Member from the entity. Removing an Owner
// requires RemoveOwner and leaves at least one Owner.
func (m Mutations[S]) RemoveParticipant(ctx context.Context, change ParticipantChange) error {
	change.Role = RoleNone
	var participant participantMutation
	var previous string
	_, err := m.run(ctx, change, &participant, func(ctx context.Context, principal *auth.UserInfo) error {
		var err error
		previous, err = participant.requireRemove(ctx, principal)
		return err
	}, func(ctx context.Context, store S) (participantResult, error) {
		if err := participant.deleteForRemoval(ctx, previous); err != nil {
			return participantResult{}, err
		}
		identityID, err := store.LockMemberIdentity(ctx, change.MemberID)
		if err != nil {
			return participantResult{}, err
		}
		return participantResult{previous: previous, identityID: identityID}, nil
	})
	return err
}

// run locks the entity row in Lock, then authorizes the active principal
// with authorize, which may read the Member's current role, in Access.
func (m Mutations[S]) run(
	ctx context.Context,
	change ParticipantChange,
	participant *participantMutation,
	authorize func(context.Context, *auth.UserInfo) error,
	apply func(context.Context, S) (participantResult, error),
) (participantResult, error) {
	result, err := mutation.Run(ctx, m.runner, mutation.StoreSpec[participantResult, S]{
		Lock: func(ctx context.Context, store S) error {
			*participant = participantMutation{spec: m.spec, checker: m.checker, store: store, entityID: change.EntityID, memberID: change.MemberID}
			return m.spec.LockRoot(ctx, store, change.EntityID)
		},
		Access: mutation.Fenced(func(ctx context.Context, _ S, lockActive func(*auth.UserInfo) (bool, error)) error {
			principal, err := participant.lockPrincipal(ctx, lockActive)
			if err != nil {
				return err
			}
			return authorize(ctx, principal)
		}),
		Change: apply,
		Audit: func(result participantResult) mutation.Audit {
			return change.Audit(auditRelationship(result.previous), auditRelationship(change.Role))
		},
		Relationships: func(result participantResult) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return m.spec.participantRelationshipMutations(change.EntityID, result.identityID, result.previous, change.Role)
		},
	})
	return result, m.spec.MapMutationError(err)
}
