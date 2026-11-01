package campaign

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/emailcontent"
)

// LoadTranslationInterchangeTarget loads one Campaign target without
// applying source fallback. The caller has already locked and authorized the
// root in the same transaction.
func LoadTranslationInterchangeTarget(
	ctx context.Context,
	joined Joined,
	campaignID string,
	targetLocale string,
) (emailcontent.InterchangeTarget, error) {
	return campaignContent.LoadInterchangeTarget(ctx, joined.Store, campaignID, targetLocale)
}

// ApplyTranslationInterchange applies one sparse Campaign target under draft
// lifecycle enforcement and the Content Document CAS.
func ApplyTranslationInterchange(
	ctx context.Context,
	joined Joined,
	campaignID string,
	sourceLocale string,
	input emailcontent.InterchangeMutation,
) (emailcontent.InterchangeMutationResult, error) {
	bound := joined.Store
	return campaignContent.ApplyInterchange(ctx, bound, campaignID, sourceLocale, input, campaignEmailContentFence(bound, campaignID))
}
