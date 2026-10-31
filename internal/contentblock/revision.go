package contentblock

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// LoadRevision returns the document's revision under a share lock, for an
// owner that checks an expected revision without loading the aggregate. The
// owning API locks its root row first, as for LoadSnapshotInTransaction. A
// missing document is ErrDocumentNotFound.
func LoadRevision(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (uuid.UUID, error) {
	var row struct {
		Revision uuid.UUID `gorm:"column:revision"`
	}
	if err := tx.WithContext(ctx).
		Table("content_document").
		Select("revision").
		Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
		Where("id = ?", documentID).
		Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return uuid.Nil, ErrDocumentNotFound
		}
		return uuid.Nil, err
	}
	return row.Revision, nil
}
