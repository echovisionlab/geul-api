package contentblock

import (
	"context"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// LocaleOwnershipConflict captures the shared revision while the caller holds
// the root lock, before its rejected transaction releases that authority.
func LocaleOwnershipConflict(ctx context.Context, tx *gorm.DB, documentID uuid.UUID, message string) error {
	var row struct{ Revision string }
	if err := tx.WithContext(ctx).Table("content_document").Select("revision").
		Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", documentID).Take(&row).Error; err != nil {
		return err
	}
	return errs.CollaborationConflict(intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_LOCALE_OWNERSHIP_CHANGED, message, errs.CollaborationState{Document: row.Revision})
}
