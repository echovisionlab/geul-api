package entityparticipant

import (
	"context"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// ParentChange moves an entity under Parent, or to the top when Parent is nil.
// Previous is the parent the change was planned from.
type ParentChange[S Store] struct {
	EntityID string
	Previous *string
	Parent   *string
	// Write stores the parent on the entity's row.
	Write func(ctx context.Context, store S, parent *string) error
	// Signal schedules the entity's content signal.
	Signal func(ctx context.Context, store S) error
}

// CanonicalParent parses a set parent ID.
func (s Spec) CanonicalParent(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	parsed, err := uuid.Parse(*value)
	if err != nil {
		return nil, domainerr.InvalidField(s.ParentColumn, "must be a valid "+s.Title+" ID")
	}
	canonical := parsed.String()
	return &canonical, nil
}

// SetParent moves the entity. A parent passes its authority down, so this is a
// participant change: the caller must manage the participants of the entity and
// of both parents it touches.
func (m Mutations[S]) SetParent(ctx context.Context, change ParentChange[S]) error {
	if sameOptionalString(change.Previous, change.Parent) {
		return m.spec.RequirePermission(ctx, m.checker, change.EntityID, m.spec.Policy.ManageParticipants)
	}
	_, err := mutation.Run(ctx, m.runner, mutation.StoreSpec[struct{}, S]{
		// The parent graph comes before the entity's row lock.
		Lock: func(ctx context.Context, store S) error {
			if err := store.LockParentGraph(ctx); err != nil {
				return err
			}
			return m.spec.LockRoot(ctx, store, change.EntityID)
		},
		Access: mutation.Fenced(func(ctx context.Context, store S, lockActive func(*auth.UserInfo) (bool, error)) error {
			current, err := m.spec.LoadParent(ctx, store, change.EntityID)
			if err != nil {
				return err
			}
			if !sameOptionalString(current, change.Previous) {
				return domainerr.Precondition(m.spec.Title + " parent changed concurrently; reload before saving")
			}
			principal, err := m.spec.LockActivePrincipal(ctx, lockActive)
			if err != nil {
				return err
			}
			for _, entityID := range change.touched() {
				if err := m.spec.RequirePrincipalPermission(ctx, m.checker, entityID, m.spec.Policy.ManageParticipants, principal); err != nil {
					return err
				}
			}
			return nil
		}),
		Change: func(ctx context.Context, store S) (struct{}, error) {
			parentID := ""
			if change.Parent != nil {
				parentID = *change.Parent
			}
			if err := m.spec.ValidateParentTransition(ctx, store, change.EntityID, parentID); err != nil {
				return struct{}{}, err
			}
			return struct{}{}, change.Write(ctx, store, change.Parent)
		},
		// Telemetry has no parent record yet.
		Audit: func(struct{}) mutation.Audit { return mutation.Unchanged() },
		Outbox: func(ctx context.Context, store S, _ struct{}) error {
			return change.Signal(ctx, store)
		},
		Relationships: func(struct{}) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			return m.spec.ReplaceParentRelationships(change.EntityID, change.Previous, change.Parent)
		},
	})
	return m.spec.MapMutationError(err)
}

// touched is every entity whose authority the change moves.
func (c ParentChange[S]) touched() []string {
	touched := []string{c.EntityID}
	for _, parent := range []*string{c.Previous, c.Parent} {
		if parent != nil {
			touched = append(touched, *parent)
		}
	}
	return touched
}

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
