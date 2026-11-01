package campaign

import (
	"context"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/emailcontent"

	"github.com/echovisionlab/geul-api/internal/failurecause"

	"github.com/echovisionlab/geul-api/internal/contentblock"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

type campaignEmailEditorProjection struct {
	Document     *contentv1.RichTextDocument
	Revision     string
	SourceLocale string
}

func loadCampaignSourceLocales(
	ctx context.Context,
	db Store,
	campaigns []Campaign,
) (map[string]string, error) {
	locales := make(map[string]string, len(campaigns))
	if len(campaigns) == 0 {
		return locales, nil
	}
	ids := make([]string, len(campaigns))
	for index := range campaigns {
		ids[index] = campaigns[index].ID
	}

	rows, err := db.CampaignSourceLocales(ctx, ids)
	if err != nil {
		return nil, err
	}
	for id, source := range rows {
		locale, normalizeErr := emailcontent.NormalizeLocale(source)
		if normalizeErr != nil {
			return nil, domainerr.Precondition("Campaign source locale is invalid").WithCause(failurecause.ServerFault(failurecause.WithAttrs(normalizeErr, slog.String("campaign_id", id))))
		}
		locales[id] = locale
	}

	if len(locales) != len(campaigns) {
		return nil, domainerr.Precondition("Campaign translation source is not initialized")
	}
	return locales, nil
}

func loadCampaignEmailEditorProjection(
	ctx context.Context,
	db Store,
	campaignID string,
) (campaignEmailEditorProjection, error) {
	documentID, err := loadCampaignEmailContentDocumentRoot(ctx, db, campaignID, false)
	if err != nil {
		return campaignEmailEditorProjection{}, err
	}
	domain, err := loadCampaignSourceContext(ctx, db, campaignID, false)
	if err != nil {
		return campaignEmailEditorProjection{}, err
	}
	snapshot, err := db.ContentSnapshot(ctx, documentID, domain.SourceLocale)
	if err != nil {
		return campaignEmailEditorProjection{}, campaignContent.NormalizeContentBlockError(err)
	}
	document, err := contentblock.SnapshotToRichTextDocument(snapshot)
	if err != nil {
		return campaignEmailEditorProjection{}, campaignContent.NormalizeContentBlockError(err)
	}
	if _, _, err := loadCampaignEmailLocaleMetadata(ctx, db, campaignID); err != nil {
		return campaignEmailEditorProjection{}, err
	}
	return campaignEmailEditorProjection{
		Document: document, Revision: snapshot.Document.Revision.String(),
		SourceLocale: domain.SourceLocale,
	}, nil
}
