package contentblock

import (
	"context"

	"gorm.io/gorm"
)

// ActiveAttachmentsReferencingFile scopes this owner's attachment authority to a File.
// fileID is a bound ID or the outer File column of a correlated preflight.
func ActiveAttachmentsReferencingFile(ctx context.Context, tx *gorm.DB, fileID any) *gorm.DB {
	return tx.WithContext(ctx).Session(&gorm.Session{NewDB: true}).Table("content_block_attachment AS ref").Where("ref.selector_kind = 'active' AND ref.file_id = ?", fileID)
}
