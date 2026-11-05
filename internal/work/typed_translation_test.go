package work

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/stretchr/testify/require"
)

func TestWorkTypedTranslationRejectsMismatchedSourceLocale(t *testing.T) {
	_, err := BuildTranslationExtractionPlan(
		&TranslationJob{EntityType: "work", EntityID: "work-a", SourceLocale: "en", TargetLocale: "ko"},
		&translation.SourceDocument{ContentBlockDocument: &contentv1.LocalizedRichTextDocument{
			Locale: "fr", LocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "fr"},
		}},
	)
	require.EqualError(t, err, "typed Work translation source locale does not match the job")
}

func TestWorkTypedTranslationAllowsEmptyCurrentTitleWhenBodyIsTranslatable(t *testing.T) {
	source := &translation.SourceDocument{
		ContentBlockDocument: &contentv1.LocalizedRichTextDocument{
			Locale: "ko",
			LocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "ko", Blocks: []*contentv1.RichTextBlockLocale{{
				BlockId: "paragraph-a",
				Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
					Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: "본문"}}}},
				}},
			}}},
		},
	}
	plan, err := BuildTranslationExtractionPlan(
		&TranslationJob{EntityType: "work", EntityID: "work-a", SourceLocale: "ko", TargetLocale: "en"},
		source,
	)
	require.NoError(t, err)
	require.Len(t, plan.Units, 1)
	require.Equal(t, "본문", plan.Units[0].SourceText)
}

func TestWorkTypedTranslationRejectsAllEmptyDocument(t *testing.T) {
	source := &translation.SourceDocument{
		ContentBlockDocument: &contentv1.LocalizedRichTextDocument{
			Locale: "en",
			LocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "en", Blocks: []*contentv1.RichTextBlockLocale{{
				BlockId: "empty",
				Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
					Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{}}}},
				}},
			}}},
		},
	}
	_, err := BuildTranslationExtractionPlan(
		&TranslationJob{EntityType: "work", EntityID: "work-a", SourceLocale: "en", TargetLocale: "ko"},
		source,
	)
	require.ErrorIs(t, err, translation.ErrNoTranslatableUnits)
}
