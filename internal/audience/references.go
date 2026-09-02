package audience

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
)

type referenceCountRow struct {
	ID    string `gorm:"column:id"`
	Count int64  `gorm:"column:count"`
}

func loadSegmentReferenceCounts(ctx context.Context, db *gorm.DB, segments []*model.AudienceSegment) error {
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
	if err := applyReferenceCounts(
		ctx,
		db,
		"campaign",
		"segment_id",
		ids,
		func(id string, count int32) { byID[id].CampaignCount = count },
	); err != nil {
		return err
	}
	if err := applyReferenceCounts(
		ctx,
		db,
		"email_delivery_run",
		"audience_segment_id",
		ids,
		func(id string, count int32) { byID[id].DeliveryRunCount = count },
	); err != nil {
		return err
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

func applyReferenceCounts(
	ctx context.Context,
	db *gorm.DB,
	table, column string,
	ids []string,
	apply func(string, int32),
) error {
	var rows []referenceCountRow
	if err := db.WithContext(ctx).
		Table(table).
		Select(column+" AS id, COUNT(*) AS count").
		Where(column+" IN ?", ids).
		Group(column).
		Scan(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		apply(row.ID, int32(row.Count))
	}
	return nil
}

func ensureSegmentMutableForActiveDelivery(tx *gorm.DB, segmentID string) error {
	var activeCampaignCount int64
	if err := tx.Model(&model.Campaign{}).Where("segment_id = ? AND status IN ?", segmentID, []string{
		manageCampaignScheduled, manageCampaignSending,
	}).Count(&activeCampaignCount).Error; err != nil {
		return errors.Internal(err)
	}
	if activeCampaignCount > 0 {
		return errors.FailedPrecondition("audience segment is used by a scheduled or sending campaign")
	}
	var activeRunCount int64
	if err := tx.Model(&model.CampaignDeliveryRun{}).Where("audience_segment_id = ? AND status IN ?", segmentID, []string{
		deliveryRunScheduled, deliveryRunSending,
	}).Count(&activeRunCount).Error; err != nil {
		return errors.Internal(err)
	}
	if activeRunCount > 0 {
		return errors.FailedPrecondition("audience segment is frozen by an active delivery run")
	}
	return nil
}
