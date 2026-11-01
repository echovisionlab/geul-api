package emailauthoring

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
)

// LoadTemplateTranslationInterchangeTarget loads one target without applying
// source fallback. The Translation application has already locked the root and
// authorized source-locale editing in the same transaction.
func LoadTemplateTranslationInterchangeTarget(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	templateID string,
	targetLocale string,
) (emailcontent.InterchangeTarget, error) {
	return emailTemplateContent.LoadInterchangeTarget(ctx, tx, store, templateID, targetLocale)
}

// ApplyTemplateTranslationInterchange applies one sparse target overlay and
// subject under the Email Template lifecycle fence and Content Document CAS.
func ApplyTemplateTranslationInterchange(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	references CampaignDeliveryReferences,
	templateID string,
	sourceLocale string,
	input emailcontent.InterchangeMutation,
) (emailcontent.InterchangeMutationResult, error) {
	return emailTemplateContent.ApplyInterchange(
		ctx, tx, store, templateID, sourceLocale, input,
		emailTemplateContentFence(references, templateID),
	)
}
