package campaign

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
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
		return CampaignDeliveryRunRef{}, errs.DependencyUnavailable("Campaign delivery")
	}
	if targetCount < 0 {
		return CampaignDeliveryRunRef{}, errs.InvalidArgumentMsg("target count cannot be negative")
	}
	var lockedCampaign model.Campaign
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&lockedCampaign, "id = ?", campaign.ID).Error; err != nil {
		return CampaignDeliveryRunRef{}, err
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
		return CampaignDeliveryRunRef{}, errs.FailedPrecondition("campaign source revision is unavailable")
	}
	var sourceLayoutID *string
	var sourceLayoutUpdatedAt *time.Time
	if campaign.LayoutID != nil && strings.TrimSpace(*campaign.LayoutID) != "" {
		if emailAuthoring == nil {
			return CampaignDeliveryRunRef{}, errs.DependencyUnavailable("Email Authoring")
		}
		layoutID := strings.TrimSpace(*campaign.LayoutID)
		layouts, err := emailAuthoring.LockLayoutsForCampaign(ctx, tx, layoutID)
		if err != nil {
			return CampaignDeliveryRunRef{}, err
		}
		layout, ok := layouts[layoutID]
		if !ok {
			return CampaignDeliveryRunRef{}, errs.FailedPrecondition("campaign email layout no longer exists")
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
		return CampaignDeliveryRunRef{}, errs.InvalidArgumentMsg(err.Error())
	}
	if err := ValidateCampaignDeliverySnapshot(snapshot); err != nil {
		return CampaignDeliveryRunRef{}, errs.InvalidArgumentMsg(err.Error())
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
		return CampaignDeliveryRunRef{}, errs.InternalMsg("Campaign delivery run identity is unavailable")
	}
	if targetCount > 0 {
		if err := delivery.StartRun(ctx, tx, run.ID, targetCount, scheduledAt.UTC()); err != nil {
			return CampaignDeliveryRunRef{}, err
		}
	}
	return run, nil
}
