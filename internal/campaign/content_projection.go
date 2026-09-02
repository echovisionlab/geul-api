package campaign

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

type campaignEmailEditorProjection struct {
	Document     *contentv1.RichTextDocument
	Revision     string
	SourceLocale string
}

func loadCampaignSourceLocales(
	ctx context.Context,
	db *gorm.DB,
	campaigns []model.Campaign,
) (map[string]string, error) {
	locales := make(map[string]string, len(campaigns))
	if len(campaigns) == 0 {
		return locales, nil
	}
	ids := make([]string, len(campaigns))
	for index := range campaigns {
		ids[index] = campaigns[index].ID
	}
	var rows []struct {
		EntityID     string `gorm:"column:entity_id"`
		SourceLocale string `gorm:"column:source_locale"`
	}
	if err := db.WithContext(ctx).
		Table("campaign").
		Select("id AS entity_id, source_locale").
		Where("id IN ?", ids).
		Find(&rows).Error; err != nil {
		return nil, errs.Internal(err)
	}
	for _, row := range rows {
		locale, normalizeErr := normalizeCampaignDocumentLocale(row.SourceLocale)
		if normalizeErr != nil {
			return nil, errs.FailedPrecondition("Campaign source locale is invalid")
		}
		locales[row.EntityID] = locale
	}
	if len(locales) != len(campaigns) {
		return nil, errs.FailedPrecondition("Campaign translation source is not initialized")
	}
	return locales, nil
}

func loadCampaignEmailEditorProjection(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	entityType string,
	entityID string,
) (campaignEmailEditorProjection, error) {
	if store == nil {
		return campaignEmailEditorProjection{}, errs.Internal(errors.New("campaign content block store is not configured"))
	}
	if err := requireCampaignContentEntity(entityType); err != nil {
		return campaignEmailEditorProjection{}, errs.Internal(err)
	}
	documentID, err := loadCampaignEmailContentDocumentID(ctx, db, entityType, entityID)
	if err != nil {
		return campaignEmailEditorProjection{}, err
	}
	domain, err := loadCampaignEmailSourceContext(ctx, db, entityType, entityID)
	if err != nil {
		return campaignEmailEditorProjection{}, err
	}
	snapshot, err := store.LoadSnapshot(ctx, db, documentID, domain.SourceLocale)
	if err != nil {
		return campaignEmailEditorProjection{}, normalizeCampaignEmailContentBlockError(entityType, err)
	}
	document, err := contentblock.SnapshotToRichTextDocument(snapshot)
	if err != nil {
		return campaignEmailEditorProjection{}, normalizeCampaignEmailContentBlockError(entityType, err)
	}
	if _, _, err := loadCampaignEmailLocaleMetadata(ctx, db, entityType, entityID); err != nil {
		return campaignEmailEditorProjection{}, err
	}
	return campaignEmailEditorProjection{
		Document: document, Revision: snapshot.Document.Revision.String(),
		SourceLocale: domain.SourceLocale,
	}, nil
}
