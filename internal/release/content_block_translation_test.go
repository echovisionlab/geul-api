package release

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestReleaseTranslationExtractionRejectsAllEmptyStableUnits(t *testing.T) {
	t.Parallel()

	source := &translation.SourceDocument{
		ContentBlockDocument: &contentv1.LocalizedRichTextDocument{
			Profile: contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT,
			Locale:  "en",
			LocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "en", Blocks: []*contentv1.RichTextBlockLocale{
				releaseTranslationParagraph(uuid.NewString(), ""),
			}},
		},
	}
	_, err := BuildTranslationExtractionPlan(
		TranslationJob{EntityType: releaseContentEntity, EntityID: uuid.NewString(), SourceLocale: "en", TargetLocale: "ko"},
		source,
	)
	require.ErrorIs(t, err, translation.ErrNoTranslatableUnits)
}

func releaseTranslationParagraph(blockID string, text string) *contentv1.RichTextBlockLocale {
	return &contentv1.RichTextBlockLocale{
		BlockId: blockID,
		Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
			Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: text}}}},
		}},
	}
}
