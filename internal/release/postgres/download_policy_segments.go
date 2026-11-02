package releasepostgres

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"gorm.io/gorm"
)

// InsertTrackDownloadPolicySegments persists the selected audience links in
// the transaction that already locked and updated the Track's policy.
func InsertTrackDownloadPolicySegments(ctx context.Context, tx *gorm.DB, trackID string, ids []string, now time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	rows := make([]model.TrackDownloadAudienceSegment, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, model.TrackDownloadAudienceSegment{TrackID: trackID, AudienceSegmentID: id, CreatedAt: now})
	}
	return tx.WithContext(ctx).Create(&rows).Error
}
