package work

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestWorkInterchangeProjectionPreservesExplicitEmptyTargets(t *testing.T) {
	source := workInterchangeTestDocument("en", "Source title", "Source body")
	plan, err := BuildTranslationExtractionPlan(&model.TranslationJob{
		EntityType: "work", EntityID: "work-a", SourceLocale: "en", TargetLocale: "ko",
	}, source)
	require.NoError(t, err)

	explicitEmpty := ""
	target := workInterchangeTestDocument("ko", "", "")
	targets, err := projectWorkInterchangeTargets(
		plan, workAIDocumentLocale{Title: &explicitEmpty}, target.ContentBlockDocument,
		workInterchangeExplicitEmptyProjector,
	)
	require.NoError(t, err)
	require.Contains(t, targets, "entity:title")
	require.Equal(t, "", targets["entity:title"].TranslatedText)
	bodyHandle := workInterchangeBodyHandle(t, plan)
	require.Contains(t, targets, bodyHandle)
	require.Equal(t, "", targets[bodyHandle].TranslatedText)
	require.NotContains(t, targets, "entity:summary")
}

func TestWorkInterchangeReplaceEmitsDeletesForOmittedTargetBlocks(t *testing.T) {
	current := &contentv1.RichTextLocaleOverlay{Locale: "ko", Blocks: []*contentv1.RichTextBlockLocale{
		{BlockId: "paragraph-a"},
		{BlockId: "paragraph-b"},
	}}
	replacement := &contentv1.RichTextLocaleOverlay{Locale: "ko", Blocks: []*contentv1.RichTextBlockLocale{
		{BlockId: "paragraph-b"},
	}}

	replace := workInterchangeBlockMutations(current, replacement, true)
	require.Len(t, replace, 2)
	require.Equal(t, "paragraph-b", replace[0].GetUpsert().GetBlock().GetBlockId())
	require.Equal(t, "paragraph-a", replace[1].GetDelete().GetBlockId())

	patch := workInterchangeBlockMutations(current, replacement, false)
	require.Len(t, patch, 1)
	require.Equal(t, "paragraph-b", patch[0].GetUpsert().GetBlock().GetBlockId())
}

func TestValidateWorkInterchangeMutationUsesCurrentStableUnitIntersection(t *testing.T) {
	source := workInterchangeTestDocument("en", "Current title", "Current body")
	plan, err := BuildTranslationExtractionPlan(&model.TranslationJob{
		EntityType: "work", EntityID: "work-a", SourceLocale: "en", TargetLocale: "ko",
	}, source)
	require.NoError(t, err)
	kept := plan.Units[0].UnitID
	valid := TranslationInterchangeMutation{
		WorkID: "work-a", SourceLocale: "en", TargetLocale: "ko",
		Mode:   managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH,
		Source: source, Plan: plan,
		Targets:     map[string]translation.UnitResult{kept: {UnitID: kept, TranslatedText: "Current"}},
		UnitHandles: []string{kept},
	}
	setWorkInterchangeTestCodec(&valid)
	require.NoError(t, validateWorkInterchangeMutation(valid))

	deleted := valid
	deleted.Targets = map[string]translation.UnitResult{
		kept: {UnitID: kept, TranslatedText: "Current"},
		"block:deleted:typed:paragraph/content": {
			UnitID: "block:deleted:typed:paragraph/content", TranslatedText: "Deleted",
		},
	}
	require.ErrorContains(t, validateWorkInterchangeMutation(deleted), "manifest")
}

func TestValidateWorkInterchangeMutationRejectsLocaleAndIdentityMismatch(t *testing.T) {
	source := workInterchangeTestDocument("en", "Source title", "Source body")
	plan, err := BuildTranslationExtractionPlan(&model.TranslationJob{
		EntityType: "work", EntityID: "work-a", SourceLocale: "en", TargetLocale: "ko",
	}, source)
	require.NoError(t, err)
	mutation := TranslationInterchangeMutation{
		WorkID: "work-a", SourceLocale: "en", TargetLocale: "en",
		Mode:   managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH,
		Source: source, Plan: plan, Targets: map[string]translation.UnitResult{},
	}
	setWorkInterchangeTestCodec(&mutation)
	require.ErrorContains(t, validateWorkInterchangeMutation(mutation), "identity")

	mutation.TargetLocale = "ko"
	mutation.WorkID = "work-b"
	require.ErrorContains(t, validateWorkInterchangeMutation(mutation), "identity")
}

func setWorkInterchangeTestCodec(mutation *TranslationInterchangeMutation) {
	mutation.ProjectTargets = workInterchangeExplicitEmptyProjector
	mutation.BuildPatch = func(
		_ *translation.ExtractionPlan,
		_, _ *contentv1.LocalizedRichTextDocument,
		_ map[string]translation.UnitResult,
	) (*contentv1.RichTextLocaleOverlay, error) {
		return &contentv1.RichTextLocaleOverlay{}, nil
	}
}

func workInterchangeExplicitEmptyProjector(
	plan *translation.ExtractionPlan,
	_ *contentv1.LocalizedRichTextDocument,
) (map[string]translation.UnitResult, error) {
	result := make(map[string]translation.UnitResult)
	for _, unit := range plan.Units {
		result[unit.UnitID] = translation.UnitResult{UnitID: unit.UnitID}
	}
	return result, nil
}

func workInterchangeTestDocument(locale, title, body string) *translation.SourceDocument {
	return &translation.SourceDocument{
		Title: title,
		ContentBlockDocument: &contentv1.LocalizedRichTextDocument{
			Locale: locale,
			LocaleOverlay: &contentv1.RichTextLocaleOverlay{
				Locale: locale,
				Blocks: []*contentv1.RichTextBlockLocale{{
					BlockId: "paragraph-a",
					Value: &contentv1.RichTextBlockLocale_Paragraph{
						Paragraph: &contentv1.ParagraphBlockLocale{Content: []*contentv1.RichTextInline{{
							Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: body}},
						}}},
					},
				}},
			},
		},
	}
}

func workInterchangeBodyHandle(t *testing.T, plan *translation.ExtractionPlan) string {
	t.Helper()
	for _, unit := range plan.Units {
		if unit.ContainerType == translation.ContainerTypeBlock {
			return unit.UnitID
		}
	}
	t.Fatal("Work body unit not found")
	return ""
}
