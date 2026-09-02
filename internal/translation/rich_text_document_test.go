package translation

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/stretchr/testify/require"
)

func TestRichTextDocumentPlanAndCandidatePreserveBlockIdentity(t *testing.T) {
	summary := "Source summary"
	source := &SourceDocument{
		Title: "Source title", Summary: &summary, ContentDocumentRevision: "revision-1",
		ContentBlockDocument: &contentv1.LocalizedRichTextDocument{
			Locale: "ko",
			LocaleOverlay: &contentv1.RichTextLocaleOverlay{
				Locale: "ko",
				Blocks: []*contentv1.RichTextBlockLocale{{
					BlockId: "block-1",
					Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
						Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
							Text: &contentv1.RichTextStyledText{Text: "Source paragraph"},
						}}},
					}},
				}},
			},
		},
	}
	job := &model.TranslationJob{
		EntityType: "post", EntityID: "post-1", SourceLocale: "ko", TargetLocale: "en",
	}

	plan, err := BuildRichTextExtractionPlan(job, source, RichTextDocumentFields{Title: true, Summary: true})
	require.NoError(t, err)
	require.Len(t, plan.Units, 3)
	results := make(map[string]UnitResult, len(plan.Units))
	for _, unit := range plan.Units {
		translated := map[string]string{
			"entity:title": "Target title", "entity:summary": "Target summary",
		}[unit.UnitID]
		if translated == "" {
			translated = "Target paragraph"
		}
		results[unit.UnitID] = UnitResult{UnitID: unit.UnitID, TranslatedText: translated}
	}

	candidate, err := BuildRichTextCandidate(plan, source, results)
	require.NoError(t, err)
	require.Equal(t, "Target title", *candidate.Title)
	require.Equal(t, "Target summary", *candidate.Summary)
	require.Equal(t, "block-1", candidate.ContentBlockLocaleOverlay.Blocks[0].GetBlockId())
	require.Equal(t, "en", candidate.ContentBlockLocaleOverlay.GetLocale())
	require.Equal(t, "revision-1", candidate.ContentDocumentRevision)
}

func TestRichTextDocumentPlanCanProtectTitleInsteadOfTranslatingIt(t *testing.T) {
	source := &SourceDocument{
		Title: "Protected title",
		ContentBlockDocument: &contentv1.LocalizedRichTextDocument{
			Locale: "ko",
			LocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "ko", Blocks: []*contentv1.RichTextBlockLocale{{
				BlockId: "block-1",
				Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
					Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
						Text: &contentv1.RichTextStyledText{Text: "Body"},
					}}},
				}},
			}}},
		},
	}
	plan, err := BuildRichTextExtractionPlan(&model.TranslationJob{
		EntityType: "program_event", EntityID: "event-1", SourceLocale: "ko", TargetLocale: "en",
	}, source, RichTextDocumentFields{})
	require.NoError(t, err)
	for _, unit := range plan.Units {
		require.NotEqual(t, "entity:title", unit.UnitID)
	}
}

func TestRichTextDocumentPlanKeepsExplicitEmptyUnitBesideTranslatableUnit(t *testing.T) {
	t.Parallel()
	source := &SourceDocument{ContentBlockDocument: &contentv1.LocalizedRichTextDocument{
		Locale: "ko",
		LocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "ko", Blocks: []*contentv1.RichTextBlockLocale{
			paragraphBlock("empty"),
			paragraphBlock("body", &contentv1.RichTextInline{Value: &contentv1.RichTextInline_Text{
				Text: &contentv1.RichTextStyledText{Text: "본문"},
			}}),
		}},
	}}
	plan, err := BuildRichTextExtractionPlan(&model.TranslationJob{
		EntityType: "post", EntityID: "post-1", SourceLocale: "ko", TargetLocale: "en",
	}, source, RichTextDocumentFields{})
	require.NoError(t, err)
	require.Len(t, plan.Units, 2)
	require.Equal(t, "", plan.Units[0].SourceText)
	require.Equal(t, "본문", plan.Units[1].SourceText)
}

func TestRichTextDocumentPlanRejectsAllExplicitEmptyUnits(t *testing.T) {
	t.Parallel()
	source := &SourceDocument{ContentBlockDocument: &contentv1.LocalizedRichTextDocument{
		Locale: "ko",
		LocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "ko", Blocks: []*contentv1.RichTextBlockLocale{
			paragraphBlock("empty-a"), paragraphBlock("empty-b"),
		}},
	}}
	_, err := BuildRichTextExtractionPlan(&model.TranslationJob{
		EntityType: "post", EntityID: "post-1", SourceLocale: "ko", TargetLocale: "en",
	}, source, RichTextDocumentFields{})
	require.ErrorIs(t, err, ErrNoTranslatableUnits)
}
