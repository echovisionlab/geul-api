package releasepostgres

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"gorm.io/gorm"
)

// InsertTrackDownloadPolicyCriteriaSet persists the selected access links in
// the transaction that already locked and updated the Track's policy.
func InsertTrackDownloadPolicyCriteriaSet(ctx context.Context, tx *gorm.DB, trackID string, ids []string, now time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	rows := make([]model.TrackDownloadMemberCriteria, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, model.TrackDownloadMemberCriteria{TrackID: trackID, MemberCriteriaID: id, CreatedAt: now})
	}
	return tx.WithContext(ctx).Create(&rows).Error
}
