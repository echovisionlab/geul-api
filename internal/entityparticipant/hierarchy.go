package entityparticipant

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
)

// LockParentGraph serializes every change to the entity hierarchy for the
// rest of the transaction.
func (s Spec) LockParentGraph(ctx context.Context, tx *gorm.DB) error {
	return tx.WithContext(ctx).
		Exec("SELECT pg_advisory_xact_lock(hashtext(?))", s.ParentGraphLock).
		Error
}

// ValidateParentTransition locks the hierarchy and checks that parentID, when
// set, names another existing entity that is not a descendant of entityID.
func (s Spec) ValidateParentTransition(ctx context.Context, tx *gorm.DB, entityID, parentID string) error {
	if err := s.LockParentGraph(ctx, tx); err != nil {
		return err
	}
	if parentID == "" {
		return nil
	}
	if parentID == entityID {
		return errs.InvalidArgument(s.ParentColumn, "cannot reference the same "+s.Name)
	}
	if _, err := uuid.Parse(parentID); err != nil {
		return errs.InvalidArgument(s.ParentColumn, "must be a valid "+s.Title+" ID")
	}
	var parentExists bool
	if err := tx.WithContext(ctx).Raw(fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %s WHERE id = ?::uuid)`, s.Table), parentID).Scan(&parentExists).Error; err != nil {
		return err
	}
	if !parentExists {
		return errs.InvalidArgument(s.ParentColumn, s.Name+" does not exist")
	}
	var cycle bool
	if err := tx.WithContext(ctx).Raw(fmt.Sprintf(`
		WITH RECURSIVE descendants(id) AS (
			SELECT id FROM %[1]s WHERE %[2]s = ?::uuid
			UNION
			SELECT child.id
			FROM %[1]s child
			JOIN descendants parent ON child.%[2]s = parent.id
		)
		SELECT EXISTS(SELECT 1 FROM descendants WHERE id = ?::uuid)
	`, s.Table, s.ParentColumn), entityID, parentID).Scan(&cycle).Error; err != nil {
		return err
	}
	if cycle {
		return errs.InvalidArgument(s.ParentColumn, "would create "+strings.ToLower(s.Article)+" hierarchy cycle")
	}
	return nil
}

// LoadParent returns the entity's current parent ID.
func (s Spec) LoadParent(ctx context.Context, db *gorm.DB, entityID string) (*string, error) {
	var row struct {
		ParentID *string `gorm:"column:parent_id"`
	}
	result := db.WithContext(ctx).
		Table(s.Table).
		Select(s.ParentColumn+" AS parent_id").
		Where("id = ?", entityID).
		Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, errs.NotFound(s.Name, entityID)
	}
	if result.Error != nil {
		return nil, errs.Internal(result.Error)
	}
	return row.ParentID, nil
}
