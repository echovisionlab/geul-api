package work

import (
	"context"
	"fmt"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type contentBlockMediaReferenceRow struct {
	BlockID       string `gorm:"column:block_id"`
	ReferencePath string `gorm:"column:reference_path"`
	FileID        string `gorm:"column:file_id"`
}

// LoadContentBlockMediaReferences returns the exact active File usages for an
// already-authorized Work Content Document.
func LoadContentBlockMediaReferences(
	ctx context.Context,
	db *gorm.DB,
	documentID uuid.UUID,
) ([]*contentv1.ContentBlockMediaItem, error) {
	if db == nil {
		return nil, fmt.Errorf("work content media database is required")
	}
	if documentID == uuid.Nil {
		return []*contentv1.ContentBlockMediaItem{}, nil
	}
	var rows []contentBlockMediaReferenceRow
	if err := db.WithContext(ctx).Raw(`
		SELECT attachment.block_id, attachment.reference_path, attachment.file_id
		FROM content_block_attachment AS attachment
		JOIN content_block AS block ON block.id = attachment.block_id
		WHERE block.document_id = ? AND attachment.selector_kind = 'active'
		ORDER BY attachment.block_id ASC, attachment.reference_path ASC
	`, documentID).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load Work Content Block attachment references: %w", err)
	}
	items := make([]*contentv1.ContentBlockMediaItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, &contentv1.ContentBlockMediaItem{
			Selector:   &contentv1.ContentBlockMediaSelector{BlockId: row.BlockID, ReferencePath: row.ReferencePath},
			Attachment: &contentv1.FileAttachment{State: &contentv1.FileAttachment_ActiveFileId{ActiveFileId: row.FileID}},
		})
	}
	return items, nil
}
