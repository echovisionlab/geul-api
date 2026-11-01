package campaign

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
)

func createCampaignDeliveryRun(
	ctx context.Context,
	tx *gorm.DB,
	campaign model.Campaign,
	scheduledAt time.Time,
	targetCount int64,
	audienceTargets CampaignAudienceTargetPort,
	contentBlocks *contentblock.Store,
	emailAuthoring CampaignEmailAuthoringPort,
	delivery CampaignDeliveryPort,
) (CampaignDeliveryRunRef, error) {
	if delivery == nil {
		return CampaignDeliveryRunRef{}, domainerr.DependencyUnavailable("Campaign delivery")
	}
	if targetCount < 0 {
		return CampaignDeliveryRunRef{}, domainerr.New(domainerr.Invalid, "", "target count cannot be negative")
	}
	observedLayoutID, lockedLayouts, err := observeAndLockCampaignLayouts(
		ctx,
		tx,
		emailAuthoring,
		campaign.ID,
		nil,
	)
	if err != nil {
		return CampaignDeliveryRunRef{}, err
	}
	var lockedCampaign model.Campaign
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		First(&lockedCampaign, "id = ?", campaign.ID).Error; err != nil {
		return CampaignDeliveryRunRef{}, err
	}
	if strings.TrimSpace(ptrStringValue(lockedCampaign.LayoutID)) != observedLayoutID {
		return CampaignDeliveryRunRef{}, domainerr.Precondition("campaign layout assignment changed; retry")
	}
	campaign = lockedCampaign
	target, err := deriveCampaignDeliveryTarget(
		ctx,
		tx,
		campaign,
		audienceTargets,
	)
	if err != nil {
		return CampaignDeliveryRunRef{}, err
	}
	if campaign.UpdatedAt.IsZero() {
		return CampaignDeliveryRunRef{}, domainerr.Precondition("campaign source revision is unavailable")
	}
	var sourceLayoutID *string
	var sourceLayoutUpdatedAt *time.Time
	if campaign.LayoutID != nil && strings.TrimSpace(*campaign.LayoutID) != "" {
		layoutID := strings.TrimSpace(*campaign.LayoutID)
		layout, ok := lockedLayouts[layoutID]
		if !ok {
			return CampaignDeliveryRunRef{}, domainerr.Precondition("campaign email layout no longer exists")
		}
		sourceLayoutID = &layout.ID
		updatedAt := layout.UpdatedAt.UTC()
		sourceLayoutUpdatedAt = &updatedAt
	}
	snapshot, err := campaignRenderSnapshot(ctx, tx, contentBlocks, campaign, emailAuthoring)
	if err != nil {
		return CampaignDeliveryRunRef{}, err
	}
	sourceCampaignUpdatedAt := campaign.UpdatedAt.UTC()
	if err := ValidateCampaignDeliveryTarget(target); err != nil {
		return CampaignDeliveryRunRef{}, domainerr.New(domainerr.Invalid, "", err.Error())
	}
	if err := ValidateCampaignDeliverySnapshot(snapshot); err != nil {
		return CampaignDeliveryRunRef{}, domainerr.New(domainerr.Invalid, "", err.Error())
	}
	if err := validateCampaignRecipientScopeContent(target.RecipientScope, snapshot); err != nil {
		return CampaignDeliveryRunRef{}, err
	}
	run, err := delivery.SealRun(
		ctx,
		tx,
		CampaignDeliveryRunDefinition{
			CampaignID:              strings.TrimSpace(campaign.ID),
			ScheduledAt:             scheduledAt.UTC(),
			SnapshotSchemaVersion:   CampaignDeliverySnapshotSchemaVersion,
			Snapshot:                snapshot,
			SourceLayoutID:          sourceLayoutID,
			AudienceSegmentID:       campaign.SegmentID,
			SourceCampaignUpdatedAt: sourceCampaignUpdatedAt,
			SourceLayoutUpdatedAt:   sourceLayoutUpdatedAt,
			Target:                  target,
		},
	)
	if err != nil {
		return CampaignDeliveryRunRef{}, err
	}
	if strings.TrimSpace(run.ID) == "" {
		return CampaignDeliveryRunRef{}, errors.New("Campaign delivery run identity is unavailable")
	}
	if targetCount > 0 {
		if err := delivery.StartRun(ctx, tx, run.ID, targetCount, scheduledAt.UTC()); err != nil {
			return CampaignDeliveryRunRef{}, err
		}
	}
	return run, nil
}
