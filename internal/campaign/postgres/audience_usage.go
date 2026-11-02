package campaignpostgres

import (
	"context"
	"slices"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	campaigndomain "github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
)

// SegmentUsage reads and releases the campaigns and delivery runs that
// target an audience segment, for Audience. Campaign owns those rows.
type SegmentUsage struct{}

var activeSegmentRunStatuses = []string{campaigndomain.CampaignDeliveryRunStatusScheduled, campaigndomain.CampaignDeliveryRunStatusSending}

// CountSegmentUsage counts the campaigns and delivery runs of each segment.
// A segment nothing uses is absent from both maps.
func (SegmentUsage) CountSegmentUsage(ctx context.Context, db *gorm.DB, segmentIDs []string) (map[string]int32, map[string]int32, error) {
	campaigns, err := countBySegment(ctx, db, "campaign", "segment_id", segmentIDs)
	if err != nil {
		return nil, nil, err
	}
	runs, err := countBySegment(ctx, db, "email_delivery_run", "audience_segment_id", segmentIDs)
	if err != nil {
		return nil, nil, err
	}
	return campaigns, runs, nil
}

func countBySegment(ctx context.Context, db *gorm.DB, table, column string, segmentIDs []string) (map[string]int32, error) {
	var rows []struct {
		ID    string `gorm:"column:id"`
		Count int64  `gorm:"column:count"`
	}
	if err := db.WithContext(ctx).
		Table(table).
		Select(column+" AS id, COUNT(*) AS count").
		Where(column+" IN ?", segmentIDs).
		Group(column).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	counts := make(map[string]int32, len(rows))
	for _, row := range rows {
		counts[row.ID] = int32(row.Count)
	}
	return counts, nil
}

// SegmentInActiveUse reports whether a scheduled or sending campaign, and
// whether a scheduled or sending delivery run, targets the segment.
func (SegmentUsage) SegmentInActiveUse(ctx context.Context, tx *gorm.DB, segmentID string) (bool, bool, error) {
	var activeCampaigns int64
	if err := tx.WithContext(ctx).Model(&model.Campaign{}).Where("segment_id = ? AND status IN ?", segmentID, []model.CampaignStatus{
		model.CampaignStatusScheduled, model.CampaignStatusSending,
	}).Count(&activeCampaigns).Error; err != nil {
		return false, false, err
	}
	if activeCampaigns > 0 {
		return true, false, nil
	}
	var activeRuns int64
	if err := tx.WithContext(ctx).Model(&model.CampaignDeliveryRun{}).
		Where("audience_segment_id = ? AND status IN ?", segmentID, activeSegmentRunStatuses).
		Count(&activeRuns).Error; err != nil {
		return false, false, err
	}
	return false, activeRuns > 0, nil
}

// LockSegmentUsage locks the segment's campaigns, then its scheduled and
// sending delivery runs, in the order scheduling locks them, and returns the
// locked IDs.
func (SegmentUsage) LockSegmentUsage(ctx context.Context, tx *gorm.DB, segmentID string) ([]string, []string, error) {
	campaignIDs, err := segmentCampaignIDs(ctx, tx, segmentID, true)
	if err != nil {
		return nil, nil, err
	}
	runIDs, err := activeSegmentRunIDs(ctx, tx, segmentID, true)
	if err != nil {
		return nil, nil, err
	}
	return campaignIDs, runIDs, nil
}

// SegmentUsageChanged reports whether the segment's campaigns or active
// delivery runs differ from the ones LockSegmentUsage locked.
func (SegmentUsage) SegmentUsageChanged(ctx context.Context, tx *gorm.DB, segmentID string, campaignIDs, runIDs []string) (bool, error) {
	current, err := segmentCampaignIDs(ctx, tx, segmentID, false)
	if err != nil {
		return false, err
	}
	if !slices.Equal(current, campaignIDs) {
		return true, nil
	}
	current, err = activeSegmentRunIDs(ctx, tx, segmentID, false)
	if err != nil {
		return false, err
	}
	return !slices.Equal(current, runIDs), nil
}

func segmentCampaignIDs(ctx context.Context, db *gorm.DB, segmentID string, lock bool) ([]string, error) {
	query := db.WithContext(ctx).Table("campaign")
	if lock {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	ids := []string{}
	err := query.Where("segment_id = ?", segmentID).Order("id ASC").Pluck("id", &ids).Error
	return ids, err
}

func activeSegmentRunIDs(ctx context.Context, db *gorm.DB, segmentID string, lock bool) ([]string, error) {
	query := db.WithContext(ctx).Table("email_delivery_run")
	if lock {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	ids := []string{}
	err := query.Where("audience_segment_id = ? AND status IN ?", segmentID, activeSegmentRunStatuses).
		Order("id ASC").Pluck("id", &ids).Error
	return ids, err
}

// ReleaseSegmentForArchive cancels the segment's scheduled delivery runs and
// returns its scheduled campaigns to draft. It returns the campaigns it
// returned to draft; LockSegmentUsage must hold their locks.
func (SegmentUsage) ReleaseSegmentForArchive(ctx context.Context, tx *gorm.DB, segmentID string, now time.Time) ([]string, error) {
	scheduled := []string{}
	if err := tx.WithContext(ctx).Table("campaign").
		Where("segment_id = ? AND status = ?", segmentID, model.CampaignStatusScheduled).
		Order("id ASC").Pluck("id", &scheduled).Error; err != nil {
		return nil, err
	}
	if err := tx.WithContext(ctx).
		Model(&model.CampaignDeliveryRun{}).
		Where("audience_segment_id = ? AND status = ?", segmentID, campaigndomain.CampaignDeliveryRunStatusScheduled).
		Updates(structured.Fields{
			"status":       campaigndomain.CampaignDeliveryRunStatusCancelled,
			"completed_at": now,
			"updated_at":   now,
		}).Error; err != nil {
		return nil, err
	}
	if err := tx.WithContext(ctx).
		Model(&model.Campaign{}).
		Where("segment_id = ? AND status = ?", segmentID, model.CampaignStatusScheduled).
		Updates(structured.Fields{
			"status":       model.CampaignStatusDraft,
			"scheduled_at": nil,
			"updated_at":   now,
		}).Error; err != nil {
		return nil, err
	}
	return scheduled, nil
}
