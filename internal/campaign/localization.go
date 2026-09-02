package campaign

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
)

type localizedCampaignContentRow struct {
	Subject *string `gorm:"column:subject"`
}

// ResolveLocalizedCampaign applies the exact requested Campaign target when a
// target exists; otherwise it returns the Campaign source locale.
// The source row remains authoritative even when target rows exist.
func ResolveLocalizedCampaign(
	ctx context.Context,
	db *gorm.DB,
	contentBlocks *contentblock.Store,
	campaign model.Campaign,
	requestedLocale string,
) (model.Campaign, string, error) {
	if contentBlocks == nil {
		return campaign, "", errs.DependencyUnavailable("Campaign content Block store")
	}
	var sourceState struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	if err := db.WithContext(ctx).
		Table("campaign").
		Select("source_locale").
		Where("id = ?", campaign.ID).
		Take(&sourceState).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return campaign, "", errs.NotFound("campaign source translation", campaign.ID)
		}
		return campaign, "", err
	}
	sourceLocale := normalizeCampaignLocale(sourceState.SourceLocale)
	if sourceLocale == "" {
		return campaign, "", errs.FailedPrecondition("Campaign source locale is unavailable")
	}
	source, err := loadCampaignLocalizedContentRow(
		ctx,
		db,
		campaign.ID,
		sourceLocale,
	)
	if err != nil {
		return campaign, sourceLocale, err
	}
	if source == nil {
		return campaign, sourceLocale, errs.NotFound("campaign source translation", campaign.ID+":"+sourceLocale)
	}
	selectedLocale := sourceLocale
	var target *localizedCampaignContentRow
	requestedLocale = normalizeCampaignLocale(requestedLocale)
	if requestedLocale != "" && requestedLocale != sourceLocale {
		target, err = loadCampaignLocalizedContentRow(
			ctx,
			db,
			campaign.ID,
			requestedLocale,
		)
		if err != nil {
			return campaign, sourceLocale, err
		}
		if target != nil {
			selectedLocale = requestedLocale
		}
	}
	documentID, err := loadCampaignEmailContentDocumentID(
		ctx,
		db,
		campaignContentEntity,
		campaign.ID,
	)
	if err != nil {
		return campaign, selectedLocale, err
	}
	materialized, err := contentBlocks.LoadAndMaterializeRichTextLocale(
		ctx,
		db,
		documentID,
		sourceLocale,
		selectedLocale,
		nil,
	)
	if err != nil {
		return campaign, selectedLocale, normalizeCampaignEmailContentBlockError(campaignContentEntity, err)
	}
	return applyCampaignLocalizedContent(campaign, source, target, materialized), selectedLocale, nil
}

func loadCampaignLocalizedContentRow(
	ctx context.Context,
	db *gorm.DB,
	campaignID string,
	locale string,
) (*localizedCampaignContentRow, error) {
	var row localizedCampaignContentRow
	if err := db.WithContext(ctx).
		Table("campaign_translation").
		Select("subject").
		Where("entity_id = ? AND locale = ?", campaignID, locale).
		Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

func applyCampaignLocalizedContent(
	campaign model.Campaign,
	source *localizedCampaignContentRow,
	target *localizedCampaignContentRow,
	materialized contentblock.MaterializedContent,
) model.Campaign {
	campaign.Subject = ptrStringValue(source.Subject)
	if target != nil && target.Subject != nil {
		campaign.Subject = *target.Subject
	}
	contentHTML := materialized.HTML
	campaign.ContentHTML = &contentHTML
	return campaign
}

func normalizeCampaignLocale(locale string) string {
	normalized := localization.NormalizeSupportedLocale(strings.TrimSpace(locale))
	if normalized == nil {
		return ""
	}
	return *normalized
}
