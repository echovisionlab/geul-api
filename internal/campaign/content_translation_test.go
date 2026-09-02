package campaign

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

func TestCampaignTranslationPlanAndCandidateOwnSubjectAndTargetOverlay(t *testing.T) {
	source := &translation.SourceDocument{
		Title:                   "Source subject",
		ContentDocumentRevision: "7fd1da5e-f0f0-4d43-b237-2ca89551a1c4",
		ContentBlockDocument: &contentv1.LocalizedRichTextDocument{
			Locale: "en",
			LocaleOverlay: &contentv1.RichTextLocaleOverlay{
				Locale: "en",
				Blocks: []*contentv1.RichTextBlockLocale{{
					BlockId: "paragraph-a",
					Value: &contentv1.RichTextBlockLocale_Paragraph{
						Paragraph: &contentv1.ParagraphBlockLocale{Content: []*contentv1.RichTextInline{{
							Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: "Source body"}},
						}}},
					},
				}},
			},
		},
	}
	job := &model.TranslationJob{
		EntityType:   campaignContentEntity,
		EntityID:     "campaign-a",
		SourceLocale: "en",
		TargetLocale: "ko",
	}

	plan, err := translation.BuildRichTextExtractionPlan(
		job,
		source,
		translation.RichTextDocumentFields{Title: true},
	)
	require.NoError(t, err)
	require.Len(t, plan.Units, 2)
	results := make(map[string]translation.UnitResult, len(plan.Units))
	for _, unit := range plan.Units {
		translated := "번역 본문"
		if unit.FieldName == "title" {
			translated = "번역 제목"
		}
		results[unit.UnitID] = translation.UnitResult{
			UnitID:         unit.UnitID,
			TranslatedText: translated,
		}
	}

	candidate, err := translation.BuildRichTextCandidate(plan, source, results)
	require.NoError(t, err)
	require.Equal(t, "번역 제목", *candidate.Title)
	require.Equal(t, source.ContentDocumentRevision, candidate.ContentDocumentRevision)
	require.Equal(t, "ko", candidate.ContentBlockLocaleOverlay.GetLocale())
	require.Equal(
		t,
		"번역 본문",
		candidate.ContentBlockLocaleOverlay.GetBlocks()[0].GetParagraph().GetContent()[0].GetText().GetText(),
	)
	require.Equal(t, "en", source.ContentBlockDocument.GetLocale())
	require.Equal(
		t,
		"Source body",
		source.ContentBlockDocument.GetLocaleOverlay().GetBlocks()[0].GetParagraph().GetContent()[0].GetText().GetText(),
	)
}
