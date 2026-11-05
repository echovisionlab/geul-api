package campaign

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/translation"
)

// LoadTranslationSourceDocument exposes the Campaign-owned typed document to
// the shared translation orchestrator.
func LoadTranslationSourceDocument(
	ctx context.Context,
	joined Joined,
	campaignID string,
) (*translation.SourceDocument, error) {
	return campaignContent.LoadTranslationSourceDocument(ctx, joined.Store, campaignID,
		func(ctx context.Context, store Store, id, sourceLocale string) (string, error) {
			return loadCampaignEmailSourceSubject(ctx, store, id, sourceLocale)
		},
	)
}
