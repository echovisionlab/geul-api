package entityparticipant

import (
	"context"
	"errors"
	"time"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Store is what the shared mechanism reads and writes of one
// participant-governed entity, bound to one transaction. Artist and Label
// embed it in their own Store; internal/entityparticipant/postgres is the
// PostgreSQL implementation over the entity's tables.
type Store interface {
	// LockEntity locks the entity row for update. A missing entity is
	// ErrEntityNotFound.
	LockEntity(ctx context.Context, entityID string) error
	// ParticipantRole is the role the Member holds on the entity, RoleOwner
	// first, or RoleNone.
	ParticipantRole(ctx context.Context, entityID, memberID string) (string, error)
	// CountOwners counts the entity's Owners, only memberID's relation when
	// it is set.
	CountOwners(ctx context.Context, entityID, memberID string) (int64, error)
	// InsertParticipant gives the Member role on the entity.
	InsertParticipant(ctx context.Context, entityID, memberID, role string, createdAt time.Time) error
	// DeleteParticipant removes the Member's role relation and reports
	// whether one was removed.
	DeleteParticipant(ctx context.Context, entityID, memberID, role string) (bool, error)
	// LockMemberIdentity locks the linked Identity of the Member a
	// participant change targets and returns it
	// (authorizationtarget.RequireLocked); its errors carry their answer.
	LockMemberIdentity(ctx context.Context, memberID string) (string, error)
	// ListParticipants lists the entity's Owners, then Managers, oldest
	// first.
	ListParticipants(ctx context.Context, entityID string) ([]Participant, error)
	// LockParentGraph serializes every change to the entity hierarchy for
	// the rest of the transaction.
	LockParentGraph(ctx context.Context) error
	// EntityExists reports whether the entity exists.
	EntityExists(ctx context.Context, entityID string) (bool, error)
	// IsDescendant reports whether candidateID is below entityID in the
	// hierarchy.
	IsDescendant(ctx context.Context, entityID, candidateID string) (bool, error)
	// EntityParent is the entity's parent ID. A missing entity is
	// ErrEntityNotFound.
	EntityParent(ctx context.Context, entityID string) (*string, error)
}

// ErrEntityNotFound is a Store's answer for a missing entity row.
var ErrEntityNotFound = errors.New("participant-governed entity not found")

// Participant is one Owner or Manager relation.
type Participant struct {
	MemberID  string
	Role      string
	CreatedAt time.Time
}

// ActorChecker answers whether an Identity other than the request
// principal may act, for a participant's effective authority.
type ActorChecker interface {
	CheckActorCan(ctx context.Context, actor policyv1.Actor, can policyv1.Can) (bool, error)
}
