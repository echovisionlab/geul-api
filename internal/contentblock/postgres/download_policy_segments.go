// Package contentblockpostgres provides transaction-bound Content Block row operations.
package contentblockpostgres

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"gorm.io/gorm"
)

// InsertAttachmentDownloadPolicyCriteriaSet persists the selected access links
// after the caller locks and updates the attachment's policy.
func InsertAttachmentDownloadPolicyCriteriaSet(ctx context.Context, tx *gorm.DB, blockID, referencePath string, ids []string, now time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	rows := make([]model.ContentBlockAttachmentDownloadMemberCriteria, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, model.ContentBlockAttachmentDownloadMemberCriteria{BlockID: blockID, ReferencePath: referencePath, MemberCriteriaID: id, CreatedAt: now})
	}
	return tx.WithContext(ctx).Create(&rows).Error
}
