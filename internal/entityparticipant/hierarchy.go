package entityparticipant

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/domainerr"
)

// ValidateParentTransition locks the hierarchy and checks that parentID, when
// set, names another existing entity that is not a descendant of entityID.
func (s Spec) ValidateParentTransition(ctx context.Context, store Store, entityID, parentID string) error {
	if err := store.LockParentGraph(ctx); err != nil {
		return err
	}
	if parentID == "" {
		return nil
	}
	if parentID == entityID {
		return domainerr.InvalidField(s.ParentColumn, "cannot reference the same "+s.Name)
	}
	if _, err := uuid.Parse(parentID); err != nil {
		return domainerr.InvalidField(s.ParentColumn, "must be a valid "+s.Title+" ID")
	}
	parentExists, err := store.EntityExists(ctx, parentID)
	if err != nil {
		return err
	}
	if !parentExists {
		return domainerr.InvalidField(s.ParentColumn, s.Name+" does not exist")
	}
	cycle, err := store.IsDescendant(ctx, entityID, parentID)
	if err != nil {
		return err
	}
	if cycle {
		return domainerr.InvalidField(s.ParentColumn, "would create "+strings.ToLower(s.Article)+" hierarchy cycle")
	}
	return nil
}

// LoadParent returns the entity's current parent ID.
func (s Spec) LoadParent(ctx context.Context, store Store, entityID string) (*string, error) {
	parent, err := store.EntityParent(ctx, entityID)
	if errors.Is(err, ErrEntityNotFound) {
		return nil, domainerr.ResourceNotFound(s.Name, entityID)
	}
	return parent, err
}
