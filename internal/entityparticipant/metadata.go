package entityparticipant

import (
	"context"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/structured"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

// NullableStringUpdate is a parsed document-metadata string mutation. Present
// is false when the field is left alone; a nil Value clears it.
type NullableStringUpdate struct {
	Present bool
	Value   *string
}

// ParseNullableStringUpdate trims a set value and rejects an empty set, a
// false clear and an empty mutation.
func ParseNullableStringUpdate(field string, input *intrav1.NullableStringMutation) (NullableStringUpdate, error) {
	if input == nil {
		return NullableStringUpdate{}, nil
	}
	switch operation := input.Operation.(type) {
	case *intrav1.NullableStringMutation_Set:
		value := strings.TrimSpace(operation.Set)
		if value == "" {
			return NullableStringUpdate{}, errs.InvalidArgument(field, "set value must not be empty; use clear instead")
		}
		return NullableStringUpdate{Present: true, Value: &value}, nil
	case *intrav1.NullableStringMutation_Clear:
		if !operation.Clear {
			return NullableStringUpdate{}, errs.InvalidArgument(field, "clear must be true")
		}
		return NullableStringUpdate{Present: true}, nil
	default:
		return NullableStringUpdate{}, errs.InvalidArgument(field, "set or clear is required")
	}
}

// Changes reports whether the update gives current a different value.
func (u NullableStringUpdate) Changes(current *string) bool {
	return u.Present && !sameOptionalString(current, u.Value)
}

// Assign stores the update under column when it changes current.
func (u NullableStringUpdate) Assign(fields structured.Fields, column string, current *string) bool {
	if !u.Changes(current) {
		return false
	}
	if u.Value == nil {
		fields[column] = nil
	} else {
		fields[column] = *u.Value
	}
	return true
}

// ParentUpdate is a document-metadata parent change checked against the
// parent read before its transaction began.
type ParentUpdate struct {
	Update   NullableStringUpdate
	previous *string
}

// PlanParentUpdate reads the entity's parent outside the transaction, which
// decides whether the save needs an authorization mutation.
func (s Spec) PlanParentUpdate(ctx context.Context, db *gorm.DB, entityID string, update NullableStringUpdate) (ParentUpdate, error) {
	previous, err := s.LoadParent(ctx, db, entityID)
	if err != nil {
		return ParentUpdate{}, err
	}
	return ParentUpdate{Update: update, previous: previous}, nil
}

// RequiresWrite reports whether the planned change writes SpiceDB parent
// relationships.
func (p ParentUpdate) RequiresWrite() bool {
	return p.Update.Changes(p.previous)
}

// CheckParentUpdate validates the planned parent change against current, the
// parent read under the transaction, and reports whether it changes. write is
// nil when the save runs without an authorization mutation.
func (s Spec) CheckParentUpdate(ctx context.Context, tx *gorm.DB, entityID string, plan ParentUpdate, current *string, write authzmutation.WriteRelationships) (bool, error) {
	changed := plan.Update.Changes(current)
	if plan.Update.Present && (!sameOptionalString(current, plan.previous) || changed != plan.RequiresWrite()) {
		return false, s.parentChangedConcurrently()
	}
	if !changed {
		return false, nil
	}
	if write == nil {
		return false, s.parentChangedConcurrently()
	}
	parentID := ""
	if plan.Update.Value != nil {
		parentID = *plan.Update.Value
	}
	return true, s.ValidateParentTransition(ctx, tx, entityID, parentID)
}

// WriteParentRelationships moves the entity's SpiceDB parent from current to
// the planned parent.
func (s Spec) WriteParentRelationships(entityID string, plan ParentUpdate, current *string, write authzmutation.WriteRelationships) error {
	apply, compensate, err := s.ReplaceParentRelationships(entityID, current, plan.Update.Value)
	if err != nil {
		return err
	}
	return write(apply, compensate)
}

func (s Spec) parentChangedConcurrently() error {
	return errs.FailedPrecondition(s.Title + " parent changed concurrently; reload before saving")
}

// ExecuteMetadataMutation runs mutate inside an authorization mutation when
// the parent changes, and inside a plain transaction with a nil write
// otherwise.
func ExecuteMetadataMutation(ctx context.Context, db *gorm.DB, spiceDB *auth.SpiceDBClient, plan ParentUpdate, mutate func(*gorm.DB, authzmutation.WriteRelationships) error) error {
	if plan.RequiresWrite() {
		_, err := authzmutation.Execute(ctx, db, spiceDB, mutate)
		return err
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return mutate(tx, nil) })
}

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
