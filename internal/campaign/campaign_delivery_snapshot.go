package campaign

import (
	"context"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/sitevariable"

	"github.com/echovisionlab/geul-api/internal/contentblock"
)

func campaignRenderSnapshot(
	ctx context.Context,
	db Store,
	campaign Campaign,
) (CampaignDeliverySnapshot, error) {

	snapshot := CampaignDeliverySnapshot{
		Subject: campaign.Subject,
		Translations: make(
			[]CampaignDeliverySnapshotTranslation,
			0,
		),
	}

	campaignRows, err := db.CampaignRenderLocales(ctx, campaign.ID)
	if err != nil {
		return CampaignDeliverySnapshot{}, err
	}

	if len(campaignRows) == 0 {
		return CampaignDeliverySnapshot{}, domainerr.Precondition(
			"campaign has no source content",
		)
	}
	sourceLocale := ""
	sourceSubject := ""
	for _, row := range campaignRows {
		if !row.IsSourceLocale {
			continue
		}
		sourceLocale = normalizeCampaignLocale(row.Locale)
		sourceSubject = ptrStringValue(row.Subject)
		break
	}
	if sourceLocale == "" {
		return CampaignDeliverySnapshot{}, domainerr.Precondition(
			"campaign source locale is unavailable",
		)
	}
	documentID, err := loadCampaignEmailContentDocumentRoot(ctx, db, campaign.ID, false)
	if err != nil {
		return CampaignDeliverySnapshot{}, err
	}
	contentSnapshot, err := db.LockedContentSnapshot(ctx, documentID, sourceLocale)
	if err != nil {
		return CampaignDeliverySnapshot{}, campaignContent.NormalizeContentBlockError(err)
	}
	for _, row := range campaignRows {
		locale := normalizeCampaignLocale(row.Locale)
		if locale == "" {
			return CampaignDeliverySnapshot{}, domainerr.Precondition(
				"campaign target locale is unavailable",
			)
		}
		localizedDocument, err := contentblock.MaterializeSnapshotRichTextLocale(contentSnapshot, locale)
		if err != nil {
			return CampaignDeliverySnapshot{}, campaignContent.NormalizeContentBlockError(err)
		}
		materialized, err := contentblock.MaterializeLocalizedRichTextDocument(ctx, localizedDocument, nil, sitevariable.TemplateContext())
		if err != nil {
			return CampaignDeliverySnapshot{}, campaignContent.NormalizeContentBlockError(err)
		}
		subject := sourceSubject
		if row.IsSourceLocale || row.Subject != nil {
			subject = ptrStringValue(row.Subject)
		}
		entry := CampaignDeliverySnapshotTranslation{
			Locale:      locale,
			Subject:     subject,
			ContentHTML: materialized.HTML,
		}
		if row.IsSourceLocale {
			snapshot.SourceLocale = locale
			snapshot.Subject = entry.Subject
			snapshot.ContentHTML = entry.ContentHTML
		}
		snapshot.Translations = append(snapshot.Translations, entry)
	}
	if err := applyEmailDeliveryLayoutSnapshot(
		ctx,
		db,
		ptrStringValue(campaign.LayoutID),
		"campaign email layout",
		&snapshot,
	); err != nil {
		return CampaignDeliverySnapshot{}, err
	}

	return snapshot, nil
}

func applyEmailDeliveryLayoutSnapshot(
	ctx context.Context,
	db Store,
	layoutID string,
	label string,
	snapshot *CampaignDeliverySnapshot,
) error {
	layoutID = strings.TrimSpace(layoutID)
	if layoutID == "" {
		return nil
	}

	rows, err := db.LayoutSnapshot(ctx, layoutID)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return domainerr.Precondition(label + " has no source content")
	}

	translations := make([]CampaignDeliverySnapshotLayout, 0, len(rows))
	sourceLocale := ""
	for _, row := range rows {
		if row.IsSourceLocale {
			sourceLocale = row.Locale
		}
		translations = append(translations, CampaignDeliverySnapshotLayout{
			Locale:      row.Locale,
			HTMLContent: row.HTMLContent,
		})
	}
	if strings.TrimSpace(sourceLocale) == "" {
		return domainerr.Precondition(label + " source locale is unavailable")
	}
	snapshot.LayoutSourceLocale = &sourceLocale
	snapshot.LayoutTranslations = &translations
	return nil
}
