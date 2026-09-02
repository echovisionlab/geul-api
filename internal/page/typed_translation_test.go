package page

import (
	"github.com/echovisionlab/geul-api/internal/translation"
	"strings"
	"testing"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestPageTypedTranslationExtractsAndAppliesTypedLocaleFields(t *testing.T) {
	caption := "  Source caption  "
	immersiveTitle := "Source scene title"
	immersiveText := "Source scene text"
	summary := "Source summary"
	source := &translation.SourceDocument{
		Title:                   "Source title",
		Summary:                 &summary,
		ContentDocumentRevision: "7fd1da5e-f0f0-4d43-b237-2ca89551a1c4",
		PageDocument: &contentv1.LocalizedPageDocument{
			BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
			Locale:                  "en",
			LocaleOverlay: &contentv1.PageLocaleOverlay{
				Locale: "en",
				Sections: []*contentv1.PageSectionLocale{
					{
						SectionId: "external-section",
						Value: &contentv1.PageSectionLocale_ExternalVideo{
							ExternalVideo: &contentv1.ExternalVideoSectionLocale{
								Props: &contentv1.ExternalVideoSectionLocaleProps{Caption: &caption},
							},
						},
					},
					{
						SectionId: "immersive-section",
						Value: &contentv1.PageSectionLocale_ImmersiveScene{
							ImmersiveScene: &contentv1.ImmersiveSceneSectionLocale{
								Units: []*contentv1.PageImmersiveUnitLocale{{
									UnitId: "unit-a",
									Props: &contentv1.PageImmersiveUnitLocaleProps{
										Title: &immersiveTitle,
										Text:  &immersiveText,
									},
								}},
							},
						},
					},
					{
						SectionId: "rich-text-section",
						Value: &contentv1.PageSectionLocale_RichText{
							RichText: &contentv1.RichTextSectionLocale{
								Blocks: &contentv1.RichTextLocaleOverlay{
									Locale: "en",
									Blocks: []*contentv1.RichTextBlockLocale{{
										BlockId: "paragraph-a",
										Value: &contentv1.RichTextBlockLocale_Paragraph{
											Paragraph: &contentv1.ParagraphBlockLocale{
												Content: []*contentv1.RichTextInline{{
													Value: &contentv1.RichTextInline_Text{
														Text: &contentv1.RichTextStyledText{Text: "Source paragraph"},
													},
												}},
											},
										},
									}},
								},
							},
						},
					},
				},
			},
		},
	}
	job := &model.TranslationJob{
		EntityType:   "page",
		EntityID:     "page-a",
		SourceLocale: "en",
		TargetLocale: "ko",
	}

	plan, err := BuildTranslationExtractionPlan(job, source)
	require.NoError(t, err)
	require.Len(t, plan.Units, 6)

	results := make(map[string]translation.UnitResult, len(plan.Units))
	translations := map[string]string{
		"Source title":     "번역 제목",
		"Source summary":   "번역 요약",
		caption:            "번역 캡션",
		immersiveTitle:     "번역 장면 제목",
		immersiveText:      "번역 장면 본문",
		"Source paragraph": "번역 문단",
	}
	for _, unit := range plan.Units {
		translated, ok := translations[unit.SourceText]
		require.True(t, ok, unit.UnitID)
		results[unit.UnitID] = translation.UnitResult{
			UnitID:         unit.UnitID,
			TranslatedText: translated,
		}
		if unit.SourceText == "Source paragraph" {
			require.Equal(t, "rich-text-section", unit.ContainerID)
			require.Contains(t, unit.UnitID, "section:rich-text-section:block:paragraph-a:typed:")
		}
	}

	candidate, err := BuildTranslationCandidate(plan, source, results)
	require.NoError(t, err)
	require.Equal(t, source.ContentDocumentRevision, candidate.ContentDocumentRevision)
	require.Equal(t, "번역 제목", derefString(candidate.Title))
	require.Equal(t, "번역 요약", derefString(candidate.Summary))
	require.Equal(t, "ko", candidate.PageDocument.GetLocale())
	require.Equal(t, "ko", candidate.PageDocument.GetLocaleOverlay().GetLocale())
	require.Equal(
		t,
		"  번역 캡션  ",
		candidate.PageDocument.GetLocaleOverlay().GetSections()[0].GetExternalVideo().GetProps().GetCaption(),
	)
	require.Equal(
		t,
		"번역 장면 제목",
		candidate.PageDocument.GetLocaleOverlay().GetSections()[1].GetImmersiveScene().GetUnits()[0].GetProps().GetTitle(),
	)
	require.Equal(
		t,
		"번역 장면 본문",
		candidate.PageDocument.GetLocaleOverlay().GetSections()[1].GetImmersiveScene().GetUnits()[0].GetProps().GetText(),
	)
	require.Equal(
		t,
		"번역 문단",
		candidate.PageDocument.GetLocaleOverlay().GetSections()[2].GetRichText().GetBlocks().GetBlocks()[0].GetParagraph().GetContent()[0].GetText().GetText(),
	)
	require.Equal(
		t,
		"ko",
		candidate.PageDocument.GetLocaleOverlay().GetSections()[2].GetRichText().GetBlocks().GetLocale(),
	)

	// Candidate construction must not mutate the captured source document.
	require.Equal(t, "en", source.PageDocument.GetLocale())
	require.Equal(t, caption, source.PageDocument.GetLocaleOverlay().GetSections()[0].GetExternalVideo().GetProps().GetCaption())
}

func TestPageTypedRichTextKeepsMarkBoundariesInsideOneSemanticUnit(t *testing.T) {
	t.Parallel()
	bold := true
	content := []*contentv1.RichTextInline{
		{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: "Hello "}}},
		{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{
			Text: "world", Styles: &contentv1.RichTextStyle{Bold: &bold},
		}}},
	}
	source := &translation.SourceDocument{PageDocument: &contentv1.LocalizedPageDocument{
		Locale: "en", LocaleOverlay: &contentv1.PageLocaleOverlay{Locale: "en", Sections: []*contentv1.PageSectionLocale{{
			SectionId: "rich", Value: &contentv1.PageSectionLocale_RichText{RichText: &contentv1.RichTextSectionLocale{
				Blocks: &contentv1.RichTextLocaleOverlay{Locale: "en", Blocks: []*contentv1.RichTextBlockLocale{{
					BlockId: "paragraph", Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{Content: content}},
				}}},
			}},
		}}},
	}}
	plan, err := BuildTranslationExtractionPlan(&model.TranslationJob{
		EntityType: "page", EntityID: "page", SourceLocale: "en", TargetLocale: "ko",
	}, source)
	require.NoError(t, err)
	require.Len(t, plan.Units, 1)
	unit := plan.Units[0]
	require.Equal(t, "Hello world", unit.SourceText)
	require.Len(t, unit.SourceInline, 2)

	result := translation.UnitResult{
		UnitID: unit.UnitID, TranslatedText: "안녕 세상", OriginalData: unit.OriginalData,
		TargetInline: []translation.XLIFFInline{
			{Kind: translation.XLIFFInlinePairedCode, ID: "r1", DataRefStart: "d1", DataRefEnd: "d2", CanCopy: "no", CanDelete: "no", Children: []translation.XLIFFInline{{Kind: translation.XLIFFInlineText, Text: "안녕 "}}},
			{Kind: translation.XLIFFInlinePairedCode, ID: "r2", DataRefStart: "d3", DataRefEnd: "d4", CanCopy: "no", CanDelete: "no", Children: []translation.XLIFFInline{{Kind: translation.XLIFFInlineText, Text: "세상"}}},
		},
	}
	candidate, err := BuildTranslationCandidate(plan, source, map[string]translation.UnitResult{unit.UnitID: result})
	require.NoError(t, err)
	translated := candidate.PageDocument.GetLocaleOverlay().GetSections()[0].GetRichText().GetBlocks().GetBlocks()[0].GetParagraph().GetContent()
	require.Equal(t, "안녕 ", translated[0].GetText().GetText())
	require.Equal(t, "세상", translated[1].GetText().GetText())
	require.True(t, translated[1].GetText().GetStyles().GetBold())
}

func TestPageTypedTranslationKeepsExplicitEmptyRichTextUnitBesideContent(t *testing.T) {
	t.Parallel()
	source := pageRichTextTranslationSource(
		pageParagraphLocale("empty"),
		pageParagraphLocale("body", "본문"),
	)
	plan, err := BuildTranslationExtractionPlan(&model.TranslationJob{
		EntityType: "page", EntityID: "page", SourceLocale: "en", TargetLocale: "ko",
	}, source)
	require.NoError(t, err)
	require.Len(t, plan.Units, 2)
	require.Equal(t, "", plan.Units[0].SourceText)
	require.Equal(t, "본문", plan.Units[1].SourceText)
}

func TestPageTypedTranslationRejectsAllExplicitEmptyRichTextUnits(t *testing.T) {
	t.Parallel()
	source := pageRichTextTranslationSource(pageParagraphLocale("empty-a"), pageParagraphLocale("empty-b"))
	_, err := BuildTranslationExtractionPlan(&model.TranslationJob{
		EntityType: "page", EntityID: "page", SourceLocale: "en", TargetLocale: "ko",
	}, source)
	require.ErrorIs(t, err, translation.ErrNoTranslatableUnits)
}

func TestProviderPageTargetPatchPreservesUnrelatedCurrentImmersiveUnit(t *testing.T) {
	t.Parallel()
	requestedSource, unrelatedSource := "requested source", "unrelated source"
	requestedTarget, unrelatedTarget := "새 번역", "기존 번역"
	sourceSection := &contentv1.PageSectionLocale{
		SectionId: "immersive", Value: &contentv1.PageSectionLocale_ImmersiveScene{
			ImmersiveScene: &contentv1.ImmersiveSceneSectionLocale{Units: []*contentv1.PageImmersiveUnitLocale{
				{UnitId: "unit-a", Props: &contentv1.PageImmersiveUnitLocaleProps{Title: &requestedSource}},
				{UnitId: "unit-b", Props: &contentv1.PageImmersiveUnitLocaleProps{Title: &unrelatedSource}},
			}},
		},
	}
	candidateSection := proto.Clone(sourceSection).(*contentv1.PageSectionLocale)
	candidateSection.GetImmersiveScene().GetUnits()[0].GetProps().Title = &requestedTarget
	currentSection := proto.Clone(sourceSection).(*contentv1.PageSectionLocale)
	currentSection.GetImmersiveScene().GetUnits()[0].GetProps().Title = nil
	currentSection.GetImmersiveScene().GetUnits()[1].GetProps().Title = &unrelatedTarget
	source := &contentv1.LocalizedPageDocument{
		Locale: "en", LocaleOverlay: &contentv1.PageLocaleOverlay{Locale: "en", Sections: []*contentv1.PageSectionLocale{sourceSection}},
	}
	current := &contentv1.LocalizedPageDocument{
		Locale: "ko", LocaleOverlay: &contentv1.PageLocaleOverlay{Locale: "ko", Sections: []*contentv1.PageSectionLocale{currentSection}},
	}
	candidate := &contentv1.LocalizedPageDocument{
		Locale: "ko", LocaleOverlay: &contentv1.PageLocaleOverlay{Locale: "ko", Sections: []*contentv1.PageSectionLocale{candidateSection}},
	}
	handle := "section:immersive:immersive-unit:unit-a:typed:title"
	patched, err := buildProviderPageTargetDocument(source, current, candidate, &translation.ProviderUnitPatch{
		Units:   []translation.Unit{{UnitID: handle, ContainerType: translation.ContainerTypeBlock, ContainerID: "immersive"}},
		Results: map[string]translation.UnitResult{handle: {UnitID: handle, TranslatedText: requestedTarget}},
	})
	require.NoError(t, err)
	units := patched.GetLocaleOverlay().GetSections()[0].GetImmersiveScene().GetUnits()
	require.Len(t, units, 2)
	require.Equal(t, requestedTarget, units[0].GetProps().GetTitle())
	require.Equal(t, unrelatedTarget, units[1].GetProps().GetTitle())
	require.NotEqual(t, unrelatedSource, units[1].GetProps().GetTitle())
}

func pageRichTextTranslationSource(blocks ...*contentv1.RichTextBlockLocale) *translation.SourceDocument {
	return &translation.SourceDocument{PageDocument: &contentv1.LocalizedPageDocument{
		Locale: "en", LocaleOverlay: &contentv1.PageLocaleOverlay{Locale: "en", Sections: []*contentv1.PageSectionLocale{{
			SectionId: "rich", Value: &contentv1.PageSectionLocale_RichText{RichText: &contentv1.RichTextSectionLocale{
				Blocks: &contentv1.RichTextLocaleOverlay{Locale: "en", Blocks: blocks},
			}},
		}}},
	}}
}

func pageParagraphLocale(blockID string, text ...string) *contentv1.RichTextBlockLocale {
	block := &contentv1.RichTextBlockLocale{
		BlockId: blockID,
		Value:   &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{}},
	}
	if len(text) != 0 {
		block.GetParagraph().Content = []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
			Text: &contentv1.RichTextStyledText{Text: text[0]},
		}}}
	}
	return block
}

func TestPageTypedTranslationUnitIDsSurviveSectionAndImmersiveUnitReorder(t *testing.T) {
	titleA := "First unit"
	titleB := "Second unit"
	sectionA := &contentv1.PageSectionLocale{
		SectionId: "section-a",
		Value: &contentv1.PageSectionLocale_ImmersiveScene{
			ImmersiveScene: &contentv1.ImmersiveSceneSectionLocale{
				Units: []*contentv1.PageImmersiveUnitLocale{
					{UnitId: "unit-a", Props: &contentv1.PageImmersiveUnitLocaleProps{Title: &titleA}},
					{UnitId: "unit-b", Props: &contentv1.PageImmersiveUnitLocaleProps{Title: &titleB}},
				},
			},
		},
	}
	sectionB := &contentv1.PageSectionLocale{
		SectionId: "section-b",
		Value: &contentv1.PageSectionLocale_ExternalVideo{
			ExternalVideo: &contentv1.ExternalVideoSectionLocale{},
		},
	}
	job := &model.TranslationJob{
		EntityType: "page", EntityID: "page-a", SourceLocale: "en", TargetLocale: "ko",
	}
	build := func(sections []*contentv1.PageSectionLocale) map[string]string {
		plan, err := BuildTranslationExtractionPlan(job, &translation.SourceDocument{
			PageDocument: &contentv1.LocalizedPageDocument{
				Locale: "en",
				LocaleOverlay: &contentv1.PageLocaleOverlay{
					Locale: "en", Sections: sections,
				},
			},
		})
		require.NoError(t, err)
		result := make(map[string]string, len(plan.Units))
		for _, unit := range plan.Units {
			result[unit.SourceText] = unit.UnitID
			require.NotContains(t, unit.UnitID, "/0/")
			require.NotContains(t, unit.UnitID, "/1/")
		}
		return result
	}

	before := build([]*contentv1.PageSectionLocale{sectionA, sectionB})
	sectionA.GetImmersiveScene().Units[0], sectionA.GetImmersiveScene().Units[1] =
		sectionA.GetImmersiveScene().Units[1], sectionA.GetImmersiveScene().Units[0]
	after := build([]*contentv1.PageSectionLocale{sectionB, sectionA})
	require.Equal(t, before, after)
	require.Contains(t, before[titleA], "immersive-unit:unit-a")
	require.Contains(t, before[titleB], "immersive-unit:unit-b")
}

func TestPageTypedTranslationMutationsFlattenSectionAndRichTextRowsSeparately(t *testing.T) {
	sectionID := uuid.NewString()
	blockID := uuid.NewString()
	document := &contentv1.LocalizedPageDocument{
		Locale: "ko",
		LocaleOverlay: &contentv1.PageLocaleOverlay{
			Locale: "ko",
			Sections: []*contentv1.PageSectionLocale{{
				SectionId: sectionID,
				Value: &contentv1.PageSectionLocale_RichText{
					RichText: &contentv1.RichTextSectionLocale{
						Blocks: &contentv1.RichTextLocaleOverlay{
							Locale: "ko",
							Blocks: []*contentv1.RichTextBlockLocale{{
								BlockId: blockID,
								Value: &contentv1.RichTextBlockLocale_Paragraph{
									Paragraph: &contentv1.ParagraphBlockLocale{},
								},
							}},
						},
					},
				},
			}},
		},
	}
	mutations, err := pageTypedTranslationLocaleMutations(document)
	require.NoError(t, err)
	require.Len(t, mutations, 2)
	require.Nil(t, mutations[0].GetUpsert().GetSection().GetRichText().GetBlocks())
	require.Equal(t, blockID, mutations[1].GetMutateRichTextBlock().GetMutation().GetUpsert().GetBlock().GetBlockId())

	batch, err := contentblock.BatchFromPageSystemProto(uuid.New(), &contentv1.PageSectionMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		ExpectedRevision:        uuid.NewString(),
		LocaleMutationGroups: []*contentv1.PageLocaleMutationGroup{{
			Locale: "ko", Mutations: mutations,
		}},
	})
	require.NoError(t, err)
	require.Len(t, batch.LocaleGroups, 1)
	require.Len(t, batch.LocaleGroups[0].Upserts, 2)
	require.Equal(t, uuid.MustParse(sectionID), batch.LocaleGroups[0].Upserts[0].BlockID)
	require.Equal(t, uuid.MustParse(blockID), batch.LocaleGroups[0].Upserts[1].BlockID)
	require.NotContains(t, strings.ToLower(string(batch.LocaleGroups[0].Upserts[0].LocalizedData)), "blocks")
	require.NotContains(t, string(batch.LocaleGroups[0].Upserts[0].LocalizedData), blockID)
}

func TestPageTypedTranslationRejectsLocaleMismatch(t *testing.T) {
	job := &model.TranslationJob{
		EntityType:   "page",
		EntityID:     "page-a",
		SourceLocale: "en",
		TargetLocale: "ko",
	}
	_, err := BuildTranslationExtractionPlan(job, &translation.SourceDocument{
		PageDocument: &contentv1.LocalizedPageDocument{
			Locale:        "fr",
			LocaleOverlay: &contentv1.PageLocaleOverlay{Locale: "fr"},
		},
	})
	require.Error(t, err)
}
