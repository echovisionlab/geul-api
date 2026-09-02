package emailauthoring

import (
	"context"

	emailauthoringdomain "github.com/echovisionlab/geul-api/internal/emailauthoring"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"gorm.io/gorm"
)

const (
	deliveryStatusScheduled = "scheduled"
	deliveryStatusSending   = "sending"
)

var activeDeliveryStatuses = []string{deliveryStatusScheduled, deliveryStatusSending}

// CampaignDeliveryReferences projects Campaign and immutable delivery-history
// references into Email Authoring's consumer-owned capability contract.
type CampaignDeliveryReferences struct{}

func NewCampaignDeliveryReferences() *CampaignDeliveryReferences {
	return &CampaignDeliveryReferences{}
}

func (*CampaignDeliveryReferences) TemplateDeliveryRunCounts(
	ctx context.Context,
	db *gorm.DB,
	templateIDs []string,
) (map[string]int32, error) {
	return groupedCounts(ctx, db, "email_delivery_run", "source_template_id", templateIDs)
}

func (*CampaignDeliveryReferences) LayoutExternalReferenceCounts(
	ctx context.Context,
	db *gorm.DB,
	layoutIDs []string,
) (map[string]emailauthoringdomain.LayoutExternalReferenceCounts, error) {
	campaigns, err := groupedCounts(ctx, db, "campaign", "layout_id", layoutIDs)
	if err != nil {
		return nil, err
	}
	deliveryRuns, err := groupedCounts(ctx, db, "email_delivery_run", "source_layout_id", layoutIDs)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]emailauthoringdomain.LayoutExternalReferenceCounts, len(layoutIDs))
	for _, id := range layoutIDs {
		counts[id] = emailauthoringdomain.LayoutExternalReferenceCounts{
			Campaigns: campaigns[id], DeliveryRuns: deliveryRuns[id],
		}
	}
	return counts, nil
}

func (*CampaignDeliveryReferences) RequireTemplateMutable(
	ctx context.Context,
	tx *gorm.DB,
	templateID string,
) error {
	return requireMutable(ctx, tx, "source_template_id", templateID, "email template")
}

func (*CampaignDeliveryReferences) RequireLayoutMutable(
	ctx context.Context,
	tx *gorm.DB,
	layoutID string,
) error {
	return requireMutable(ctx, tx, "source_layout_id", layoutID, "email layout")
}

func (*CampaignDeliveryReferences) DetachTemplateHistory(
	ctx context.Context,
	tx *gorm.DB,
	templateID string,
) error {
	return detachTerminalHistory(ctx, tx, "source_template_id", templateID)
}

func (*CampaignDeliveryReferences) DetachLayoutHistory(
	ctx context.Context,
	tx *gorm.DB,
	layoutID string,
) error {
	return detachTerminalHistory(ctx, tx, "source_layout_id", layoutID)
}

func groupedCounts(
	ctx context.Context,
	db *gorm.DB,
	table string,
	column string,
	ids []string,
) (map[string]int32, error) {
	counts := make(map[string]int32, len(ids))
	if len(ids) == 0 {
		return counts, nil
	}
	var rows []struct {
		ID    string `gorm:"column:id"`
		Count int64  `gorm:"column:count"`
	}
	if err := db.WithContext(ctx).Table(table).
		Select(column+" AS id, COUNT(*) AS count").
		Where(column+" IN ?", ids).
		Group(column).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		counts[row.ID] = int32(row.Count)
	}
	return counts, nil
}

func requireMutable(
	ctx context.Context,
	tx *gorm.DB,
	column string,
	sourceID string,
	sourceName string,
) error {
	var count int64
	if err := tx.WithContext(ctx).Table("email_delivery_run").
		Where(column+" = ? AND status IN ?", sourceID, activeDeliveryStatuses).
		Count(&count).Error; err != nil {
		return errs.Internal(err)
	}
	if count > 0 {
		return errs.FailedPrecondition(sourceName + " is frozen by an active delivery run")
	}
	return nil
}

func detachTerminalHistory(
	ctx context.Context,
	tx *gorm.DB,
	column string,
	sourceID string,
) error {
	return tx.WithContext(ctx).Table("email_delivery_run").
		Where(column+" = ? AND status NOT IN ?", sourceID, activeDeliveryStatuses).
		Update(column, nil).Error
}

var _ emailauthoringdomain.CampaignDeliveryReferences = (*CampaignDeliveryReferences)(nil)
