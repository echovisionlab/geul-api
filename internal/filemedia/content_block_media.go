package filemedia

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

// LoadContentBlockMediaReferences returns the exact active File usages for one
// already-authorized Content Document. Callers may layer runtime delivery onto
// these items, but must not merge it into the persisted Block payload.
func LoadContentBlockMediaReferences(
	ctx context.Context,
	db *gorm.DB,
	documentID uuid.UUID,
) ([]*contentv1.ContentBlockMediaItem, error) {
	if db == nil {
		return nil, fmt.Errorf("database is required")
	}
	if documentID == uuid.Nil {
		return []*contentv1.ContentBlockMediaItem{}, nil
	}

	var rows []contentBlockMediaReferenceRow
	if err := db.WithContext(ctx).Raw(`
		SELECT cbf.block_id, cbf.reference_path, cbf.file_id
		FROM content_block_attachment AS cbf
		JOIN content_block AS cb ON cb.id = cbf.block_id
		WHERE cb.document_id = ? AND cbf.selector_kind = 'active'
		ORDER BY cbf.block_id ASC, cbf.reference_path ASC
	`, documentID).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load active Content Block attachment references: %w", err)
	}

	items := make([]*contentv1.ContentBlockMediaItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, &contentv1.ContentBlockMediaItem{
			Selector: &contentv1.ContentBlockMediaSelector{
				BlockId:       row.BlockID,
				ReferencePath: row.ReferencePath,
			},
			Attachment: &contentv1.FileAttachment{
				State: &contentv1.FileAttachment_ActiveFileId{ActiveFileId: row.FileID},
			},
		})
	}
	return items, nil
}
