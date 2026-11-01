package campaign

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
)

// LoadTranslationInterchangeTarget loads one Campaign target without
// applying source fallback. The caller has already locked and authorized the
// root in the same transaction.
func LoadTranslationInterchangeTarget(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	campaignID string,
	targetLocale string,
) (emailcontent.InterchangeTarget, error) {
	return campaignContent.LoadInterchangeTarget(ctx, bindContent(tx, store), campaignID, targetLocale)
}

// ApplyTranslationInterchange applies one sparse Campaign target under draft
// lifecycle enforcement and the Content Document CAS.
func ApplyTranslationInterchange(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	campaignID string,
	sourceLocale string,
	input emailcontent.InterchangeMutation,
) (emailcontent.InterchangeMutationResult, error) {
	bound := bindContent(tx, store)
	return campaignContent.ApplyInterchange(ctx, bound, campaignID, sourceLocale, input, contentFence(bound, campaignEmailContentFence(campaignID)))
}
