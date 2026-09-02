package campaign

import (
	"context"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
)

type campaignRenderSnapshotTranslationRow struct {
	Locale         string  `gorm:"column:locale"`
	Subject        *string `gorm:"column:subject"`
	IsSourceLocale bool    `gorm:"column:is_source_locale"`
}

func campaignRenderSnapshot(
	ctx context.Context,
	db *gorm.DB,
	contentBlocks *contentblock.Store,
	campaign model.Campaign,
	emailAuthoring CampaignEmailAuthoringPort,
) (CampaignDeliverySnapshot, error) {
	if contentBlocks == nil {
		return CampaignDeliverySnapshot{}, errs.DependencyUnavailable("Campaign content Block store")
	}
	snapshot := CampaignDeliverySnapshot{
		Subject: campaign.Subject,
		Translations: make(
			[]CampaignDeliverySnapshotTranslation,
			0,
		),
	}

	var campaignRows []campaignRenderSnapshotTranslationRow
	if err := db.WithContext(ctx).
		Table("campaign_translation AS translation").
		Select("translation.locale, translation.subject, translation.locale = source.source_locale AS is_source_locale").
		Joins("JOIN campaign AS source ON source.id = translation.entity_id").
		Where("translation.entity_id = ?", campaign.ID).
		Order("is_source_locale DESC, locale ASC").
		Scan(&campaignRows).Error; err != nil {
		return CampaignDeliverySnapshot{}, err
	}
	if len(campaignRows) == 0 {
		return CampaignDeliverySnapshot{}, errs.FailedPrecondition(
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
		return CampaignDeliverySnapshot{}, errs.FailedPrecondition(
			"campaign source locale is unavailable",
		)
	}
	documentID, err := loadCampaignEmailContentDocumentID(
		ctx,
		db,
		campaignContentEntity,
		campaign.ID,
	)
	if err != nil {
		return CampaignDeliverySnapshot{}, err
	}
	contentSnapshot, err := contentBlocks.LoadSnapshotInTransaction(ctx, db, documentID, sourceLocale)
	if err != nil {
		return CampaignDeliverySnapshot{}, normalizeCampaignEmailContentBlockError(campaignContentEntity, err)
	}
	for _, row := range campaignRows {
		locale := normalizeCampaignLocale(row.Locale)
		if locale == "" {
			return CampaignDeliverySnapshot{}, errs.FailedPrecondition(
				"campaign target locale is unavailable",
			)
		}
		localizedDocument, err := contentblock.MaterializeSnapshotRichTextLocale(contentSnapshot, locale)
		if err != nil {
			return CampaignDeliverySnapshot{}, normalizeCampaignEmailContentBlockError(campaignContentEntity, err)
		}
		materialized, err := contentblock.MaterializeLocalizedRichTextDocument(ctx, localizedDocument, nil)
		if err != nil {
			return CampaignDeliverySnapshot{}, normalizeCampaignEmailContentBlockError(campaignContentEntity, err)
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
		emailAuthoring,
		&snapshot,
	); err != nil {
		return CampaignDeliverySnapshot{}, err
	}

	return snapshot, nil
}

func applyEmailDeliveryLayoutSnapshot(
	ctx context.Context,
	db *gorm.DB,
	layoutID string,
	label string,
	emailAuthoring CampaignEmailAuthoringPort,
	snapshot *CampaignDeliverySnapshot,
) error {
	layoutID = strings.TrimSpace(layoutID)
	if layoutID == "" {
		return nil
	}

	if emailAuthoring == nil {
		return errs.DependencyUnavailable("Email Authoring")
	}
	rows, err := emailAuthoring.LoadLayoutSnapshot(ctx, db, layoutID)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return errs.FailedPrecondition(label + " has no source content")
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
		return errs.FailedPrecondition(label + " source locale is unavailable")
	}
	snapshot.LayoutSourceLocale = &sourceLocale
	snapshot.LayoutTranslations = &translations
	return nil
}
