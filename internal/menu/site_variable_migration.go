package menu

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// MigrateSiteVariableURLs runs the migration under the same root/document locks
// and advances the same revision as a normal source save. The caller owns the
// transaction and backs up both the root and document before rewriting rows.
func MigrateSiteVariableURLs(ctx context.Context, tx *gorm.DB, menuID string, rewrite func(documentID uuid.UUID) error) error {
	root, err := lockMenuForUpdate(ctx, tx, menuID)
	if err != nil {
		return err
	}
	document, err := loadMenuContentDocumentStateFromRoot(ctx, tx, *root, true)
	if err != nil {
		return err
	}
	if err := rewrite(document.ID); err != nil {
		return err
	}
	now := time.Now().UTC()
	if err := tx.WithContext(ctx).Table("menu").Where("id = ?", menuID).UpdateColumn("updated_at", now).Error; err != nil {
		return err
	}
	err = advanceMenuContentDocument(ctx, tx, menuID, document.ID, document.Revision, now)
	return err
}
