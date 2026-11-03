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

// CriteriaUsage reads and releases the campaigns and delivery runs that
// target a Member criteria, for Criteria. Campaign owns those rows.
type CriteriaUsage struct{}

var activeCriteriaRunStatuses = []string{campaigndomain.CampaignDeliveryRunStatusScheduled, campaigndomain.CampaignDeliveryRunStatusSending}

// CountCriteriaUsage counts the campaigns and delivery runs of each criteria.
// A criteria nothing uses is absent from both maps.
func (CriteriaUsage) CountCriteriaUsage(ctx context.Context, db *gorm.DB, criteriaIDs []string) (map[string]int32, map[string]int32, error) {
	campaigns, err := countByCriteria(ctx, db, "campaign", "criteria_id", criteriaIDs)
	if err != nil {
		return nil, nil, err
	}
	runs, err := countByCriteria(ctx, db, "email_delivery_run", "member_criteria_id", criteriaIDs)
	if err != nil {
		return nil, nil, err
	}
	return campaigns, runs, nil
}

func countByCriteria(ctx context.Context, db *gorm.DB, table, column string, criteriaIDs []string) (map[string]int32, error) {
	var rows []struct {
		ID    string `gorm:"column:id"`
		Count int64  `gorm:"column:count"`
	}
	if err := db.WithContext(ctx).
		Table(table).
		Select(column+" AS id, COUNT(*) AS count").
		Where(column+" IN ?", criteriaIDs).
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

// CriteriaInActiveUse reports whether a scheduled or sending campaign, and
// whether a scheduled or sending delivery run, targets the criteria.
func (CriteriaUsage) CriteriaInActiveUse(ctx context.Context, tx *gorm.DB, criteriaID string) (bool, bool, error) {
	var activeCampaigns int64
	if err := tx.WithContext(ctx).Model(&model.Campaign{}).Where("criteria_id = ? AND status IN ?", criteriaID, []model.CampaignStatus{
		model.CampaignStatusScheduled, model.CampaignStatusSending,
	}).Count(&activeCampaigns).Error; err != nil {
		return false, false, err
	}
	if activeCampaigns > 0 {
		return true, false, nil
	}
	var activeRuns int64
	if err := tx.WithContext(ctx).Model(&model.CampaignDeliveryRun{}).
		Where("member_criteria_id = ? AND status IN ?", criteriaID, activeCriteriaRunStatuses).
		Count(&activeRuns).Error; err != nil {
		return false, false, err
	}
	return false, activeRuns > 0, nil
}

// LockCriteriaUsage locks the criteria's campaigns, then its scheduled and
// sending delivery runs, in the order scheduling locks them, and returns the
// locked IDs.
func (CriteriaUsage) LockCriteriaUsage(ctx context.Context, tx *gorm.DB, criteriaID string) ([]string, []string, error) {
	campaignIDs, err := criteriaCampaignIDs(ctx, tx, criteriaID, true)
	if err != nil {
		return nil, nil, err
	}
	runIDs, err := activeCriteriaRunIDs(ctx, tx, criteriaID, true)
	if err != nil {
		return nil, nil, err
	}
	return campaignIDs, runIDs, nil
}

// CriteriaUsageChanged reports whether the criteria's campaigns or active
// delivery runs differ from the ones LockCriteriaUsage locked.
func (CriteriaUsage) CriteriaUsageChanged(ctx context.Context, tx *gorm.DB, criteriaID string, campaignIDs, runIDs []string) (bool, error) {
	current, err := criteriaCampaignIDs(ctx, tx, criteriaID, false)
	if err != nil {
		return false, err
	}
	if !slices.Equal(current, campaignIDs) {
		return true, nil
	}
	current, err = activeCriteriaRunIDs(ctx, tx, criteriaID, false)
	if err != nil {
		return false, err
	}
	return !slices.Equal(current, runIDs), nil
}

func criteriaCampaignIDs(ctx context.Context, db *gorm.DB, criteriaID string, lock bool) ([]string, error) {
	query := db.WithContext(ctx).Table("campaign")
	if lock {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	ids := []string{}
	err := query.Where("criteria_id = ?", criteriaID).Order("id ASC").Pluck("id", &ids).Error
	return ids, err
}

func activeCriteriaRunIDs(ctx context.Context, db *gorm.DB, criteriaID string, lock bool) ([]string, error) {
	query := db.WithContext(ctx).Table("email_delivery_run")
	if lock {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	ids := []string{}
	err := query.Where("member_criteria_id = ? AND status IN ?", criteriaID, activeCriteriaRunStatuses).
		Order("id ASC").Pluck("id", &ids).Error
	return ids, err
}

// ReleaseCriteriaForArchive cancels the criteria's scheduled delivery runs and
// returns its scheduled campaigns to draft. It returns the campaigns it
// returned to draft; LockCriteriaUsage must hold their locks.
func (CriteriaUsage) ReleaseCriteriaForArchive(ctx context.Context, tx *gorm.DB, criteriaID string, now time.Time) ([]string, error) {
	scheduled := []string{}
	if err := tx.WithContext(ctx).Table("campaign").
		Where("criteria_id = ? AND status = ?", criteriaID, model.CampaignStatusScheduled).
		Order("id ASC").Pluck("id", &scheduled).Error; err != nil {
		return nil, err
	}
	if err := tx.WithContext(ctx).
		Model(&model.CampaignDeliveryRun{}).
		Where("member_criteria_id = ? AND status = ?", criteriaID, campaigndomain.CampaignDeliveryRunStatusScheduled).
		Updates(structured.Fields{
			"status":       campaigndomain.CampaignDeliveryRunStatusCancelled,
			"completed_at": now,
			"updated_at":   now,
		}).Error; err != nil {
		return nil, err
	}
	if err := tx.WithContext(ctx).
		Model(&model.Campaign{}).
		Where("criteria_id = ? AND status = ?", criteriaID, model.CampaignStatusScheduled).
		Updates(structured.Fields{
			"status":       model.CampaignStatusDraft,
			"scheduled_at": nil,
			"updated_at":   now,
		}).Error; err != nil {
		return nil, err
	}
	return scheduled, nil
}
