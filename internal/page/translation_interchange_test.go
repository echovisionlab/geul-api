package page

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestPageInterchangeProjectionPreservesStableBlockExplicitEmpty(t *testing.T) {
	source := pageInterchangeTestDocument("en", "Source title", "Source body")
	plan, err := BuildTranslationExtractionPlan(&model.TranslationJob{
		EntityType: "page", EntityID: "page-a", SourceLocale: "en", TargetLocale: "ko",
	}, source)
	require.NoError(t, err)

	explicitEmpty := ""
	target := pageInterchangeTestDocument("ko", "", "")
	targets, err := projectPageInterchangeTargets(
		plan, pageAIDocumentLocale{Title: &explicitEmpty}, target.PageDocument,
		pageInterchangeExplicitEmptyProjector,
		func(_ protoreflect.Message, _ []string) bool { return false },
		func(result translation.UnitResult) translation.UnitResult { return result },
		func(inline []translation.XLIFFInline) []translation.XLIFFInline { return inline },
	)
	require.NoError(t, err)
	require.Contains(t, targets, "entity:title")
	require.Equal(t, "", targets["entity:title"].TranslatedText)
	bodyHandle := pageInterchangeBodyHandle(t, plan)
	require.Contains(t, bodyHandle, "section:section-a:block:paragraph-a:typed:")
	require.Contains(t, targets, bodyHandle)
	require.Equal(t, "", targets[bodyHandle].TranslatedText)
}

func TestValidatePageInterchangeMutationUsesCurrentStableUnitIntersection(t *testing.T) {
	source := pageInterchangeTestDocument("en", "Current title", "Current body")
	plan, err := BuildTranslationExtractionPlan(&model.TranslationJob{
		EntityType: "page", EntityID: "page-a", SourceLocale: "en", TargetLocale: "ko",
	}, source)
	require.NoError(t, err)
	kept := plan.Units[0].UnitID
	valid := TranslationInterchangeMutation{
		PageID: "page-a", SourceLocale: "en", TargetLocale: "ko",
		Mode:   managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH,
		Source: source, Plan: plan,
		Targets:     map[string]translation.UnitResult{kept: {UnitID: kept, TranslatedText: "Current"}},
		UnitHandles: []string{kept},
	}
	setPageInterchangeTestCodec(&valid)
	require.NoError(t, validatePageInterchangeMutation(valid))

	deleted := valid
	deleted.Targets = map[string]translation.UnitResult{
		kept: {UnitID: kept, TranslatedText: "Current"},
		"section:deleted:block:deleted:typed:paragraph/content": {
			UnitID: "section:deleted:block:deleted:typed:paragraph/content", TranslatedText: "Deleted",
		},
	}
	require.ErrorContains(t, validatePageInterchangeMutation(deleted), "manifest")
}

func TestValidatePageInterchangeMutationRejectsLocaleAndIdentityMismatch(t *testing.T) {
	source := pageInterchangeTestDocument("en", "Source title", "Source body")
	plan, err := BuildTranslationExtractionPlan(&model.TranslationJob{
		EntityType: "page", EntityID: "page-a", SourceLocale: "en", TargetLocale: "ko",
	}, source)
	require.NoError(t, err)
	mutation := TranslationInterchangeMutation{
		PageID: "page-a", SourceLocale: "en", TargetLocale: "en",
		Mode:   managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH,
		Source: source, Plan: plan, Targets: map[string]translation.UnitResult{},
	}
	setPageInterchangeTestCodec(&mutation)
	require.ErrorContains(t, validatePageInterchangeMutation(mutation), "identity")

	mutation.TargetLocale = "ko"
	mutation.PageID = "page-b"
	require.ErrorContains(t, validatePageInterchangeMutation(mutation), "identity")
}

func TestPatchPageContainerInterchangePreservesUnselectedImmersiveSibling(t *testing.T) {
	oldTitle, oldText := "기존 제목", "기존 본문"
	newTitle, newText := "Source title", "새 본문"
	section := func(title, text string) *contentv1.PageSectionLocale {
		return &contentv1.PageSectionLocale{
			SectionId: "scene", Value: &contentv1.PageSectionLocale_ImmersiveScene{
				ImmersiveScene: &contentv1.ImmersiveSceneSectionLocale{Units: []*contentv1.PageImmersiveUnitLocale{{
					UnitId: "unit-a", Props: &contentv1.PageImmersiveUnitLocaleProps{Title: &title, Text: &text},
				}}},
			},
		}
	}
	candidate := &contentv1.LocalizedPageDocument{LocaleOverlay: &contentv1.PageLocaleOverlay{
		Sections: []*contentv1.PageSectionLocale{section(newTitle, newText)},
	}}
	current := &contentv1.LocalizedPageDocument{LocaleOverlay: &contentv1.PageLocaleOverlay{
		Sections: []*contentv1.PageSectionLocale{section(oldTitle, oldText)},
	}}
	err := patchPageContainerInterchange(
		candidate, current,
		[]string{"section:scene:immersive-unit:unit-a:typed:text"},
		managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH,
		func(destination, source protoreflect.Message, path []string) error {
			require.Equal(t, []string{"immersive_scene", "units", "unit-a", "props", "text"}, path)
			destinationSection := destination.Interface().(*contentv1.PageSectionLocale)
			sourceSection := source.Interface().(*contentv1.PageSectionLocale)
			value := sourceSection.GetImmersiveScene().GetUnits()[0].GetProps().GetText()
			destinationSection.GetImmersiveScene().GetUnits()[0].Props.Text = &value
			return nil
		},
	)
	require.NoError(t, err)
	props := candidate.GetLocaleOverlay().GetSections()[0].GetImmersiveScene().GetUnits()[0].GetProps()
	require.Equal(t, oldTitle, props.GetTitle())
	require.Equal(t, newText, props.GetText())

	replacement := &contentv1.LocalizedPageDocument{LocaleOverlay: &contentv1.PageLocaleOverlay{
		Sections: []*contentv1.PageSectionLocale{section(newTitle, newText)},
	}}
	err = patchPageContainerInterchange(
		replacement, current,
		[]string{"section:scene:immersive-unit:unit-a:typed:text"},
		managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE,
		func(destination, source protoreflect.Message, _ []string) error {
			destinationSection := destination.Interface().(*contentv1.PageSectionLocale)
			sourceSection := source.Interface().(*contentv1.PageSectionLocale)
			value := sourceSection.GetImmersiveScene().GetUnits()[0].GetProps().GetText()
			destinationSection.Value = &contentv1.PageSectionLocale_ImmersiveScene{
				ImmersiveScene: &contentv1.ImmersiveSceneSectionLocale{Units: []*contentv1.PageImmersiveUnitLocale{{
					UnitId: "unit-a", Props: &contentv1.PageImmersiveUnitLocaleProps{Text: &value},
				}}},
			}
			return nil
		},
	)
	require.NoError(t, err)
	replacementProps := replacement.GetLocaleOverlay().GetSections()[0].GetImmersiveScene().GetUnits()[0].GetProps()
	require.Nil(t, replacementProps.Title)
	require.Equal(t, newText, replacementProps.GetText())
}

func TestPageInterchangeReplacementDeletesOmittedSectionsAndBlocks(t *testing.T) {
	richTextSection := func(sectionID string, blockIDs ...string) *contentv1.PageSectionLocale {
		blocks := make([]*contentv1.RichTextBlockLocale, 0, len(blockIDs))
		for _, blockID := range blockIDs {
			blocks = append(blocks, &contentv1.RichTextBlockLocale{BlockId: blockID})
		}
		return &contentv1.PageSectionLocale{
			SectionId: sectionID,
			Value: &contentv1.PageSectionLocale_RichText{RichText: &contentv1.RichTextSectionLocale{
				Blocks: &contentv1.RichTextLocaleOverlay{Locale: "en", Blocks: blocks},
			}},
		}
	}
	current := &contentv1.LocalizedPageDocument{LocaleOverlay: &contentv1.PageLocaleOverlay{
		Locale: "en", Sections: []*contentv1.PageSectionLocale{
			richTextSection("removed-section", "removed-with-section"),
			richTextSection("kept-section", "kept-block", "removed-block"),
		},
	}}
	replacement := &contentv1.LocalizedPageDocument{LocaleOverlay: &contentv1.PageLocaleOverlay{
		Locale: "en", Sections: []*contentv1.PageSectionLocale{
			richTextSection("kept-section", "kept-block"),
		},
	}}

	mutations := pageInterchangeReplacementDeletes(current, replacement)
	require.Len(t, mutations, 2)
	require.Equal(t, "removed-block", mutations[0].GetMutateRichTextBlock().GetMutation().GetDelete().GetBlockId())
	require.Equal(t, "removed-section", mutations[1].GetDelete().GetSectionId())
}

func setPageInterchangeTestCodec(mutation *TranslationInterchangeMutation) {
	mutation.ProjectTargets = pageInterchangeExplicitEmptyProjector
	mutation.BuildPatch = func(
		_ *translation.ExtractionPlan,
		_, _ *contentv1.LocalizedRichTextDocument,
		_ map[string]translation.UnitResult,
	) (*contentv1.RichTextLocaleOverlay, error) {
		return &contentv1.RichTextLocaleOverlay{}, nil
	}
	mutation.PathPresent = func(_ protoreflect.Message, _ []string) bool { return false }
	mutation.CloneResult = func(result translation.UnitResult) translation.UnitResult { return result }
	mutation.EmptyInline = func(inline []translation.XLIFFInline) []translation.XLIFFInline { return inline }
	mutation.CopyPath = func(_, _ protoreflect.Message, _ []string) error { return nil }
}

func pageInterchangeExplicitEmptyProjector(
	plan *translation.ExtractionPlan,
	_ *contentv1.LocalizedRichTextDocument,
) (map[string]translation.UnitResult, error) {
	result := make(map[string]translation.UnitResult)
	for _, unit := range plan.Units {
		result[unit.UnitID] = translation.UnitResult{UnitID: unit.UnitID}
	}
	return result, nil
}

func pageInterchangeTestDocument(locale, title, body string) *translation.SourceDocument {
	return &translation.SourceDocument{
		Title: title,
		PageDocument: &contentv1.LocalizedPageDocument{
			BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
			Locale:                  locale,
			Base: &contentv1.PageSectionGraph{
				Nodes: []*contentv1.PageSectionNode{
					{
						Section: &contentv1.PageSection{
							Id: "section-a",
							Value: &contentv1.PageSection_RichText{
								RichText: &contentv1.RichTextSection{
									Blocks: &contentv1.RichTextBlockGraph{
										Nodes: []*contentv1.RichTextBlockNode{
											{
												Block: &contentv1.RichTextBlock{
													Id: "paragraph-a",
													Value: &contentv1.RichTextBlock_Paragraph{
														Paragraph: &contentv1.ParagraphBlock{},
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			LocaleOverlay: &contentv1.PageLocaleOverlay{
				Locale: locale,
				Sections: []*contentv1.PageSectionLocale{{
					SectionId: "section-a",
					Value: &contentv1.PageSectionLocale_RichText{
						RichText: &contentv1.RichTextSectionLocale{
							Blocks: &contentv1.RichTextLocaleOverlay{
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
					},
				}},
			},
		},
	}
}

func pageInterchangeBodyHandle(t *testing.T, plan *translation.ExtractionPlan) string {
	t.Helper()
	for _, unit := range plan.Units {
		if unit.ContainerType == translation.ContainerTypeBlock {
			return unit.UnitID
		}
	}
	t.Fatal("Page body unit not found")
	return ""
}
