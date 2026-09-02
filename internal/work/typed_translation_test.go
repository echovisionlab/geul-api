package work

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/stretchr/testify/require"
)

func TestWorkTypedTranslationUsesBlockOwnedStableScope(t *testing.T) {
	summary := "Source summary"
	source := &translation.SourceDocument{
		Title:                   "Source title",
		Summary:                 &summary,
		ContentDocumentRevision: "7fd1da5e-f0f0-4d43-b237-2ca89551a1c4",
		ContentBlockDocument: &contentv1.LocalizedRichTextDocument{
			Locale: "en",
			LocaleOverlay: &contentv1.RichTextLocaleOverlay{
				Locale: "en",
				Blocks: []*contentv1.RichTextBlockLocale{{
					BlockId: "paragraph-a",
					Value: &contentv1.RichTextBlockLocale_Paragraph{
						Paragraph: &contentv1.ParagraphBlockLocale{Content: []*contentv1.RichTextInline{{
							Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: "Source paragraph"}},
						}}},
					},
				}},
			},
		},
	}
	job := &model.TranslationJob{
		EntityType: "work", EntityID: "work-a", SourceLocale: "en", TargetLocale: "ko",
	}

	plan, err := BuildTranslationExtractionPlan(job, source)
	require.NoError(t, err)
	require.Len(t, plan.Units, 3)

	results := make(map[string]translation.UnitResult, len(plan.Units))
	for _, unit := range plan.Units {
		translated := map[string]string{
			"Source title":     "번역 제목",
			"Source summary":   "번역 요약",
			"Source paragraph": "번역 문단",
		}[unit.SourceText]
		require.NotEmpty(t, translated)
		results[unit.UnitID] = translation.UnitResult{UnitID: unit.UnitID, TranslatedText: translated}
		if unit.SourceText == "Source paragraph" {
			require.Equal(t, "paragraph-a", unit.ContainerID)
			require.Contains(t, unit.UnitID, "block:paragraph-a:typed:")
			require.Contains(t, unit.Path, "block:paragraph-a:")
		}
	}

	candidate, err := BuildTranslationCandidate(plan, source, results)
	require.NoError(t, err)
	require.Equal(t, "번역 제목", *candidate.Title)
	require.Equal(t, "번역 요약", *candidate.Summary)
	require.Equal(t, source.ContentDocumentRevision, candidate.ContentDocumentRevision)
	require.Equal(t, "ko", candidate.ContentBlockLocaleOverlay.GetLocale())
	require.Equal(
		t,
		"번역 문단",
		candidate.ContentBlockLocaleOverlay.GetBlocks()[0].GetParagraph().GetContent()[0].GetText().GetText(),
	)

	// Candidate construction must not mutate the captured source document.
	require.Equal(t, "en", source.ContentBlockDocument.GetLocale())
	require.Equal(t, "Source paragraph", source.ContentBlockDocument.GetLocaleOverlay().GetBlocks()[0].GetParagraph().GetContent()[0].GetText().GetText())
}

func TestWorkTypedTranslationRejectsMismatchedSourceLocale(t *testing.T) {
	_, err := BuildTranslationExtractionPlan(
		&model.TranslationJob{EntityType: "work", EntityID: "work-a", SourceLocale: "en", TargetLocale: "ko"},
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
		&model.TranslationJob{EntityType: "work", EntityID: "work-a", SourceLocale: "ko", TargetLocale: "en"},
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
		&model.TranslationJob{EntityType: "work", EntityID: "work-a", SourceLocale: "en", TargetLocale: "ko"},
		source,
	)
	require.ErrorIs(t, err, translation.ErrNoTranslatableUnits)
}
