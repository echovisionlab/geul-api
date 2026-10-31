package entityparticipant

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// Mutations runs the entity's participant and metadata mutations through the
// mutation pipeline.
type Mutations struct {
	spec    Spec
	runner  *mutation.Runner
	spiceDB *auth.SpiceDBClient
}

// Mutations returns the entity's pipeline mutations.
func (s Spec) Mutations(runner *mutation.Runner, spiceDB *auth.SpiceDBClient) Mutations {
	return Mutations{spec: s, runner: runner, spiceDB: spiceDB}
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
func (m Mutations) SetParticipant(ctx context.Context, members Members, change ParticipantChange) (View, error) {
	var participant participantMutation
	var previous string
	result, err := m.run(ctx, change, &participant, func(principal *auth.UserInfo) error {
		var err error
		if previous, err = participant.currentRole(); err != nil {
			return err
		}
		return participant.requireManage(ctx, principal, previous, change.Role)
	}, func(ctx context.Context, tx *gorm.DB) (participantResult, error) {
		if previous != RoleNone && previous == change.Role {
			return participantResult{}, participant.alreadyHasRole()
		}
		target, err := authorizationtarget.RequireLocked(ctx, tx, change.MemberID)
		if err != nil {
			return participantResult{}, err
		}
		if previous == RoleOwner {
			if err := participant.requireAnotherOwner(); err != nil {
				return participantResult{}, err
			}
		}
		if previous != RoleNone {
			if err := participant.deleteRelation(previous); err != nil {
				return participantResult{}, err
			}
		}
		createdAt, err := participant.insertRelation(change.Role)
		return participantResult{previous: previous, identityID: target.IdentityID, createdAt: createdAt}, err
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
func (m Mutations) RemoveParticipant(ctx context.Context, change ParticipantChange) error {
	change.Role = RoleNone
	var participant participantMutation
	var previous string
	_, err := m.run(ctx, change, &participant, func(principal *auth.UserInfo) error {
		var err error
		previous, err = participant.requireRemove(ctx, principal)
		return err
	}, func(ctx context.Context, tx *gorm.DB) (participantResult, error) {
		if err := participant.deleteForRemoval(previous); err != nil {
			return participantResult{}, err
		}
		target, err := authorizationtarget.RequireLocked(ctx, tx, change.MemberID)
		if err != nil {
			return participantResult{}, err
		}
		return participantResult{previous: previous, identityID: target.IdentityID}, nil
	})
	return err
}

// run locks the entity row in Lock, then authorizes the active principal
// with authorize, which may read the Member's current role, in Access.
func (m Mutations) run(
	ctx context.Context,
	change ParticipantChange,
	participant *participantMutation,
	authorize func(*auth.UserInfo) error,
	apply func(context.Context, *gorm.DB) (participantResult, error),
) (participantResult, error) {
	result, err := mutation.Run(ctx, m.runner, mutation.Spec[participantResult]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			*participant = participantMutation{spec: m.spec, spiceDB: m.spiceDB, tx: tx, entityID: change.EntityID, memberID: change.MemberID}
			return m.spec.LockRoot(ctx, tx, change.EntityID)
		},
		Access: mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
			principal, err := participant.lockPrincipal(ctx)
			if err != nil {
				return err
			}
			return authorize(principal)
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
