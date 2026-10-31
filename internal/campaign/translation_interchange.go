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
	return campaignContent.LoadInterchangeTarget(ctx, tx, store, campaignID, targetLocale)
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
	return campaignContent.ApplyInterchange(ctx, tx, store, campaignID, sourceLocale, input, campaignEmailContentFence(campaignID))
}
