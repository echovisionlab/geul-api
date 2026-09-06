package release

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestBuildTranslationCandidateLeavesPostRequestBlocksMissing(t *testing.T) {
	t.Parallel()

	requestedID := uuid.NewString()
	addedLaterID := uuid.NewString()
	source := &translation.SourceDocument{
		Title:                   "Release",
		ContentDocumentRevision: uuid.NewString(),
		ContentBlockDocument: &contentv1.LocalizedRichTextDocument{
			Profile: contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT, Locale: "en",
			LocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "en", Blocks: []*contentv1.RichTextBlockLocale{
				releaseTranslationParagraph(requestedID, "Requested"),
				releaseTranslationParagraph(addedLaterID, "Added later"),
			}},
		},
	}
	job := &model.TranslationJob{EntityType: releaseContentEntity, EntityID: uuid.NewString(), SourceLocale: "en", TargetLocale: "ko"}
	plan, err := BuildTranslationExtractionPlan(job, source)
	require.NoError(t, err)
	requestedUnits := make([]translation.Unit, 0, 1)
	for _, unit := range plan.Units {
		if unit.ContainerID == requestedID {
			requestedUnits = append(requestedUnits, unit)
		}
	}
	require.Len(t, requestedUnits, 1)
	plan.Units = requestedUnits
	plan.Bundles = translation.BuildBundles(job.EntityType, job.EntityID, job.SourceLocale, job.TargetLocale, requestedUnits, nil)

	candidate, err := BuildTranslationCandidate(plan, source, map[string]translation.UnitResult{
		requestedUnits[0].UnitID: {UnitID: requestedUnits[0].UnitID, TranslatedText: "번역"},
	})
	require.NoError(t, err)
	require.Len(t, candidate.ContentBlockLocaleOverlay.GetBlocks(), 1)
	require.Equal(t, requestedID, candidate.ContentBlockLocaleOverlay.GetBlocks()[0].GetBlockId())
	require.Equal(t, "번역", candidate.ContentBlockLocaleOverlay.GetBlocks()[0].GetParagraph().GetContent()[0].GetText().GetText())
}

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
		&model.TranslationJob{EntityType: releaseContentEntity, EntityID: uuid.NewString(), SourceLocale: "en", TargetLocale: "ko"},
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
