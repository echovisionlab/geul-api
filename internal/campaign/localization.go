package campaign

import (
	"context"
	"errors"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/sitevariable"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/localization"
)

type localizedCampaignContentRow struct {
	Subject *string
}

// ResolveLocalizedCampaign applies the exact requested Campaign target when a
// target exists; otherwise it returns the Campaign source locale.
// The source row remains authoritative even when target rows exist.
func ResolveLocalizedCampaign(
	ctx context.Context,
	db Store,
	campaign Campaign,
	requestedLocale string,
) (Campaign, string, error) {

	sourceState, err := db.CampaignSourceLocale(ctx, campaign.ID, false)
	if errors.Is(err, ErrCampaignNotFound) {
		return campaign, "", domainerr.ResourceNotFound("campaign source translation", campaign.ID)
	}
	if err != nil {
		return campaign, "", err
	}

	sourceLocale := normalizeCampaignLocale(sourceState)
	if sourceLocale == "" {
		return campaign, "", domainerr.Precondition("Campaign source locale is unavailable")
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
		return campaign, sourceLocale, domainerr.ResourceNotFound("campaign source translation", campaign.ID+":"+sourceLocale)
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
	documentID, err := loadCampaignEmailContentDocumentRoot(ctx, db, campaign.ID, false)
	if err != nil {
		return campaign, selectedLocale, err
	}

	snapshot, err := db.ContentSnapshot(ctx, documentID, sourceLocale)
	if err != nil {
		return campaign, selectedLocale, campaignContent.NormalizeContentBlockError(err)
	}
	document, err := contentblock.MaterializeSnapshotRichTextLocale(snapshot, selectedLocale)
	if err != nil {
		return campaign, selectedLocale, campaignContent.NormalizeContentBlockError(err)
	}
	materialized, err := contentblock.MaterializeLocalizedRichTextDocument(ctx, document, nil, sitevariable.TemplateContext())
	if err != nil {
		return campaign, selectedLocale, campaignContent.NormalizeContentBlockError(err)
	}

	return applyCampaignLocalizedContent(campaign, source, target, materialized), selectedLocale, nil
}

func loadCampaignLocalizedContentRow(ctx context.Context, db Store, campaignID, locale string) (*localizedCampaignContentRow, error) {
	subject, exists, err := db.CampaignLocaleSubject(ctx, campaignID, locale, false)
	if err != nil || !exists {
		return nil, err
	}
	return &localizedCampaignContentRow{Subject: subject}, nil
}

func applyCampaignLocalizedContent(
	campaign Campaign,
	source *localizedCampaignContentRow,
	target *localizedCampaignContentRow,
	materialized contentblock.MaterializedContent,
) Campaign {
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
