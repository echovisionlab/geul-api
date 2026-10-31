package audience

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
)

type referenceCountRow struct {
	ID    string `gorm:"column:id"`
	Count int64  `gorm:"column:count"`
}

func (s *Service) loadSegmentReferenceCounts(ctx context.Context, db *gorm.DB, segments []*model.AudienceSegment) error {
	byID := make(map[string]*model.AudienceSegment, len(segments))
	ids := make([]string, 0, len(segments))
	for _, segment := range segments {
		if segment == nil {
			continue
		}
		segment.CampaignCount = 0
		segment.DeliveryRunCount = 0
		segment.DownloadPolicyReferenceCount = 0
		byID[segment.ID] = segment
		ids = append(ids, segment.ID)
	}
	if len(ids) == 0 {
		return nil
	}
	campaigns, runs, err := s.campaigns.CountSegmentUsage(ctx, db, ids)
	if err != nil {
		return err
	}
	for id, count := range campaigns {
		byID[id].CampaignCount = count
	}
	for id, count := range runs {
		byID[id].DeliveryRunCount = count
	}
	return applyDownloadPolicyReferenceCounts(ctx, db, ids, func(id string, count int32) {
		byID[id].DownloadPolicyReferenceCount = count
	})
}

func applyDownloadPolicyReferenceCounts(
	ctx context.Context,
	db *gorm.DB,
	ids []string,
	apply func(string, int32),
) error {
	var rows []referenceCountRow
	if err := db.WithContext(ctx).Raw(`
		SELECT audience_segment_id AS id, COUNT(*) AS count
		FROM (
			SELECT audience_segment_id
			FROM content_block_attachment_download_audience_segment
			WHERE audience_segment_id IN ?
			UNION ALL
			SELECT audience_segment_id
			FROM track_download_audience_segment
			WHERE audience_segment_id IN ?
		) AS policy_reference
		GROUP BY audience_segment_id
	`, ids, ids).Scan(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		apply(row.ID, int32(row.Count))
	}
	return nil
}

func (s *Service) ensureSegmentMutableForActiveDelivery(ctx context.Context, tx *gorm.DB, segmentID string) error {
	campaign, deliveryRun, err := s.campaigns.SegmentInActiveUse(ctx, tx, segmentID)
	if err != nil {
		return err
	}
	if campaign {
		return domainerr.Precondition("audience segment is used by a scheduled or sending campaign")
	}
	if deliveryRun {
		return domainerr.Precondition("audience segment is frozen by an active delivery run")
	}
	return nil
}
