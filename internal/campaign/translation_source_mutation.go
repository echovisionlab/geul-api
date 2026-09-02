package campaign

import (
	"context"
	"strings"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RequireTranslationSourceMutable owns the Campaign root lock and the exact
// lifecycle fences that must hold before Translation mutates Campaign source
// state.
func RequireTranslationSourceMutable(
	ctx context.Context,
	tx *gorm.DB,
	campaignID string,
) error {
	campaignID = strings.TrimSpace(campaignID)
	var campaign model.Campaign
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "status").
		First(&campaign, "id = ?", campaignID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return errs.NotFound("campaign", campaignID)
		}
		return err
	}
	if !campaignStatusAllowsEdit(campaign.Status) {
		return errs.FailedPrecondition(errs.MsgCampaignCannotUpdateSent)
	}

	var activeRunCount int64
	if err := tx.WithContext(ctx).
		Model(&model.CampaignDeliveryRun{}).
		Where(
			"campaign_id = ? AND status IN ?",
			campaignID,
			[]string{
				CampaignDeliveryRunStatusScheduled,
				CampaignDeliveryRunStatusSending,
			},
		).
		Count(&activeRunCount).Error; err != nil {
		return err
	}
	if activeRunCount > 0 {
		return errs.FailedPrecondition(
			"campaign is frozen by an active delivery run",
		)
	}
	return nil
}
