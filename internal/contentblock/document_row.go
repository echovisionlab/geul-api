package contentblock

import (
	"context"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// LockDocumentRow locks the content document row for the rest of tx, for an
// owner that must hold the document before reading its snapshot.
func LockDocumentRow(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) error {
	var document Document
	return tx.WithContext(ctx).Table("content_document").
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", documentID).Take(&document).Error
}

// CurrentRevision reads the document's current revision.
func CurrentRevision(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (uuid.UUID, error) {
	var current struct {
		Revision uuid.UUID `gorm:"column:revision"`
	}
	err := tx.WithContext(ctx).Table("content_document").Select("revision").Where("id = ?", documentID).Take(&current).Error
	return current.Revision, err
}
