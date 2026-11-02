// Package contentblockpostgres provides transaction-bound Content Block row operations.
package contentblockpostgres

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"gorm.io/gorm"
)

// InsertAttachmentDownloadPolicySegments persists the selected audience links
// after the caller locks and updates the attachment's policy.
func InsertAttachmentDownloadPolicySegments(ctx context.Context, tx *gorm.DB, blockID, referencePath string, ids []string, now time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	rows := make([]model.ContentBlockAttachmentDownloadAudienceSegment, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, model.ContentBlockAttachmentDownloadAudienceSegment{BlockID: blockID, ReferencePath: referencePath, AudienceSegmentID: id, CreatedAt: now})
	}
	return tx.WithContext(ctx).Create(&rows).Error
}
