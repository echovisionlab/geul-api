package emailauthoring

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/emailcontent"
)

// LoadTemplateTranslationInterchangeTarget reads one Email Template target
// without source fallback, through store in the translation domain's
// transaction.
func LoadTemplateTranslationInterchangeTarget(
	ctx context.Context,
	store Store,
	templateID string,
	targetLocale string,
) (emailcontent.InterchangeTarget, error) {
	return emailTemplateContent.LoadInterchangeTarget(ctx, store, templateID, targetLocale)
}

// ApplyTemplateTranslationInterchange applies one XLIFF target behind the
// template's delivery lifecycle fence, through store in the translation
// domain's transaction.
func ApplyTemplateTranslationInterchange(
	ctx context.Context,
	store Store,
	templateID string,
	sourceLocale string,
	input emailcontent.InterchangeMutation,
) (emailcontent.InterchangeMutationResult, error) {
	return emailTemplateContent.ApplyInterchange(
		ctx, store, templateID, sourceLocale, input,
		emailTemplateContentFence(store, templateID),
	)
}
