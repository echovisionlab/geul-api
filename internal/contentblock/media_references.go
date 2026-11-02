package contentblock

import (
	"context"
	"fmt"

	fileref "github.com/echovisionlab/geul-api/internal/fileref"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type mediaReferenceRow struct {
	BlockID       string `gorm:"column:block_id"`
	ReferencePath string `gorm:"column:reference_path"`
	FileID        string `gorm:"column:file_id"`
}

// LoadMediaReferences returns the exact active File usages for one
// already-authorized Content Document. Callers may layer runtime delivery onto
// these items, but must not merge it into the persisted Block payload.
func LoadMediaReferences(
	ctx context.Context,
	db *gorm.DB,
	documentID uuid.UUID,
) ([]*fileref.BlockMediaItem, error) {
	if db == nil {
		return nil, fmt.Errorf("database is required")
	}
	if documentID == uuid.Nil {
		return []*fileref.BlockMediaItem{}, nil
	}
	var rows []mediaReferenceRow
	if err := db.WithContext(ctx).Raw(`
		SELECT attachment.block_id, attachment.reference_path, attachment.file_id
		FROM content_block_attachment AS attachment
		JOIN content_block AS block ON block.id = attachment.block_id
		WHERE block.document_id = ? AND attachment.selector_kind = 'active'
		ORDER BY attachment.block_id ASC, attachment.reference_path ASC
	`, documentID).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load active Content Block attachment references: %w", err)
	}
	items := make([]*fileref.BlockMediaItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, &fileref.BlockMediaItem{
			Selector:   &fileref.BlockMediaSelector{BlockID: row.BlockID, ReferencePath: row.ReferencePath},
			Attachment: &fileref.Attachment{Kind: fileref.AttachmentActive, FileID: row.FileID},
		})
	}
	return items, nil
}
