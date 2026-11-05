package translation

import (
	"testing"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/stretchr/testify/require"
)

func TestRichTextUnitsPreserveStyledRunsWhenApplied(t *testing.T) {
	t.Parallel()
	bold := true
	block := paragraphBlock("block-1",
		&contentv1.RichTextInline{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: "Hello "}}},
		&contentv1.RichTextInline{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{
			Text: "world", Styles: &contentv1.RichTextStyle{Bold: &bold},
		}}},
	)
	units, err := ExtractRichTextUnits(block, RichTextUnitScope{
		EntityType: "post", EntityID: "post-1", SourceLocale: "en",
		ContainerID: "block-1", UnitPrefix: "block:block-1", PathPrefix: "block:block-1",
	})
	require.NoError(t, err)
	require.Len(t, units, 1)
	require.Equal(t, "Hello world", units[0].SourceText)
	require.Len(t, units[0].SourceInline, 2)

	result := UnitResult{
		UnitID: units[0].UnitID, TranslatedText: "안녕 세상", OriginalData: units[0].OriginalData,
		TargetInline: []XLIFFInline{
			{
				Kind: XLIFFInlinePairedCode, ID: "r1", DataRefStart: "d1", DataRefEnd: "d2",
				CanCopy: "no", CanDelete: "no", Children: []XLIFFInline{{Kind: XLIFFInlineText, Text: "안녕 "}},
			},
			{
				Kind: XLIFFInlinePairedCode, ID: "r2", DataRefStart: "d3", DataRefEnd: "d4",
				CanCopy: "no", CanDelete: "no", Children: []XLIFFInline{{Kind: XLIFFInlineText, Text: "세상"}},
			},
		},
	}
	require.NoError(t, ApplyRichTextResults(block, "block:block-1", map[string]UnitResult{result.UnitID: result}))
	content := block.GetParagraph().GetContent()
	require.Equal(t, "안녕 ", content[0].GetText().GetText())
	require.Equal(t, "세상", content[1].GetText().GetText())
	require.True(t, content[1].GetText().GetStyles().GetBold())
}

func TestRichTextResultsKeepTheEdgeWhitespaceTheTranslatorWrote(t *testing.T) {
	t.Parallel()
	block := paragraphBlock("block-1",
		&contentv1.RichTextInline{Value: &contentv1.RichTextInline_Link{Link: &contentv1.RichTextLink{
			Href: "mailto:support@example.test", Content: []*contentv1.RichTextStyledText{{Text: "support@example.test"}},
		}}},
		&contentv1.RichTextInline{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: "으로 보내 주세요."}}},
	)
	units, err := ExtractRichTextUnits(block, RichTextUnitScope{
		EntityType: "page", EntityID: "page-1", SourceLocale: "ko",
		ContainerID: "block-1", UnitPrefix: "block:block-1", PathPrefix: "block:block-1",
	})
	require.NoError(t, err)
	require.Len(t, units, 1)

	target := []XLIFFInline{units[0].SourceInline[0], units[0].SourceInline[1]}
	target[1].Children = []XLIFFInline{{Kind: XLIFFInlineText, Text: " is where to send it."}}
	translatedText, err := ProjectXLIFFInline(target, units[0].OriginalData)
	require.NoError(t, err)
	result := UnitResult{
		UnitID: units[0].UnitID, TranslatedText: translatedText,
		OriginalData: units[0].OriginalData, TargetInline: target,
	}
	require.NoError(t, ApplyRichTextResults(block, "block:block-1", map[string]UnitResult{result.UnitID: result}))

	content := block.GetParagraph().GetContent()
	require.Equal(t, "support@example.test", content[0].GetLink().GetContent()[0].GetText())
	require.Equal(t, " is where to send it.", content[1].GetText().GetText())
}

func TestRichTextResultsReorderCodesWithoutChangingProtectedStructure(t *testing.T) {
	t.Parallel()
	bold := true
	block := paragraphBlock("block-1",
		&contentv1.RichTextInline{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: "Ending"}}},
		&contentv1.RichTextInline{Value: &contentv1.RichTextInline_Link{Link: &contentv1.RichTextLink{
			Href: "https://example.test/fixed", Content: []*contentv1.RichTextStyledText{{Text: "Link"}},
		}}},
		&contentv1.RichTextInline{Value: &contentv1.RichTextInline_HardBreak{HardBreak: &contentv1.RichTextHardBreak{}}},
		&contentv1.RichTextInline{Value: &contentv1.RichTextInline_MathInline{MathInline: &contentv1.RichTextInlineMath{Source: "E=mc^2"}}},
		&contentv1.RichTextInline{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{
			Text: "Bold", Styles: &contentv1.RichTextStyle{Bold: &bold},
		}}},
	)
	units, err := ExtractRichTextUnits(block, RichTextUnitScope{
		EntityType: "post", EntityID: "post-1", SourceLocale: "en",
		ContainerID: "block-1", UnitPrefix: "block:block-1", PathPrefix: "block:block-1",
	})
	require.NoError(t, err)
	require.Len(t, units, 1)

	target := []XLIFFInline{
		units[0].SourceInline[1], units[0].SourceInline[2], units[0].SourceInline[4],
		units[0].SourceInline[3], units[0].SourceInline[0],
	}
	translations := map[string]string{"r1": "마침", "r2": "링크", "r3": "굵게"}
	var translateRuns func([]XLIFFInline)
	translateRuns = func(nodes []XLIFFInline) {
		for index := range nodes {
			if translated, ok := translations[nodes[index].ID]; ok {
				nodes[index].Children = []XLIFFInline{{Kind: XLIFFInlineText, Text: translated}}
			}
			translateRuns(nodes[index].Children)
		}
	}
	translateRuns(target)
	translatedText, err := ProjectXLIFFInline(target, units[0].OriginalData)
	require.NoError(t, err)
	result := UnitResult{
		UnitID: units[0].UnitID, TranslatedText: translatedText,
		OriginalData: units[0].OriginalData, TargetInline: target,
	}
	require.NoError(t, ApplyRichTextResults(block, "block:block-1", map[string]UnitResult{result.UnitID: result}))

	content := block.GetParagraph().GetContent()
	require.Len(t, content, 5)
	require.Equal(t, "https://example.test/fixed", content[0].GetLink().GetHref())
	require.Equal(t, "링크", content[0].GetLink().GetContent()[0].GetText())
	require.NotNil(t, content[1].GetHardBreak())
	require.True(t, content[2].GetText().GetStyles().GetBold())
	require.Equal(t, "굵게", content[2].GetText().GetText())
	require.Equal(t, "E=mc^2", content[3].GetMathInline().GetSource())
	require.Equal(t, "마침", content[4].GetText().GetText())
}

func TestRichTextTableUnitHandleUsesStableRowAndCellIDs(t *testing.T) {
	t.Parallel()
	block := &contentv1.RichTextBlockLocale{
		BlockId: "table-1",
		Value: &contentv1.RichTextBlockLocale_Table{Table: &contentv1.TableBlockLocale{
			Content: &contentv1.RichTextTableLocale{Rows: []*contentv1.RichTextTableRowLocale{{
				RowId: "row-stable", Cells: []*contentv1.RichTextTableCellLocale{{
					CellId: "cell-stable", Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
						Text: &contentv1.RichTextStyledText{Text: "Cell"},
					}}},
				}},
			}}},
		}},
	}

	units, err := ExtractRichTextUnits(block, RichTextUnitScope{
		EntityType: "post", EntityID: "post-1", SourceLocale: "en",
		ContainerID: "table-1", UnitPrefix: "block:table-1", PathPrefix: "block:table-1",
	})
	require.NoError(t, err)
	require.Len(t, units, 1)
	require.Equal(t, "block:table-1:typed:table/content/rows/row-stable/cells/cell-stable/content", units[0].UnitID)
	require.NotContains(t, units[0].UnitID, "/0/")
}

func TestRichTextExtractionPreservesExplicitEmptyStableParagraph(t *testing.T) {
	t.Parallel()
	units, err := ExtractRichTextUnits(paragraphBlock("empty-block"), RichTextUnitScope{
		EntityType: "post", EntityID: "post-1", SourceLocale: "en",
		ContainerID: "empty-block", UnitPrefix: "block:empty-block", PathPrefix: "block:empty-block",
	})
	require.NoError(t, err)
	require.Len(t, units, 1)
	require.Equal(t, "", units[0].SourceText)
	require.Equal(t, "block:empty-block:typed:paragraph/content", units[0].UnitID)
}

func paragraphBlock(id string, content ...*contentv1.RichTextInline) *contentv1.RichTextBlockLocale {
	return &contentv1.RichTextBlockLocale{
		BlockId: id,
		Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
			Content: content,
		}},
	}
}

func TestTranslationLineBreakRoundTripCanonicalAndLegacy(t *testing.T) {
	for _, source := range [][]*contentv1.RichTextInline{
		{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: "\na\r\n\nb\n"}}}},
		{{Value: &contentv1.RichTextInline_HardBreak{HardBreak: &contentv1.RichTextHardBreak{}}},
			{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: "a"}}},
			{Value: &contentv1.RichTextInline_HardBreak{HardBreak: &contentv1.RichTextHardBreak{}}},
			{Value: &contentv1.RichTextInline_HardBreak{HardBreak: &contentv1.RichTextHardBreak{}}},
			{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: "b"}}},
			{Value: &contentv1.RichTextInline_HardBreak{HardBreak: &contentv1.RichTextHardBreak{}}}},
	} {
		block := paragraphBlock("breaks", source...)
		units, err := ExtractRichTextUnits(block, RichTextUnitScope{EntityType: "post", EntityID: "p", SourceLocale: "en", ContainerID: "breaks", UnitPrefix: "b", PathPrefix: "b"})
		require.NoError(t, err)
		require.Len(t, units, 1)
		unit := units[0]
		require.Equal(t, "\na\n\nb\n", unit.SourceText)
		// Provider returns a newline inside an original styled run as well as the
		// protected break placeholders. Both must survive, without duplication.
		target := unit.SourceInline
		for index := range target {
			if target[index].ID == "r1" {
				target[index].Children = []XLIFFInline{{Kind: XLIFFInlineText, Text: "가\n나"}}
			}
			if target[index].ID == "r2" {
				target[index].Children = []XLIFFInline{{Kind: XLIFFInlineText, Text: "다"}}
			}
		}
		translated, err := ProjectXLIFFInline(target, unit.OriginalData)
		require.NoError(t, err)
		require.Equal(t, "\n가\n나\n\n다\n", translated)
		require.NoError(t, ApplyRichTextResults(block, "b", map[string]UnitResult{unit.UnitID: {UnitID: unit.UnitID, TranslatedText: translated, TargetInline: target, OriginalData: unit.OriginalData}}))
		content := block.GetParagraph().GetContent()
		require.Len(t, content, 8)
		for _, index := range []int{0, 2, 4, 5, 7} {
			require.NotNil(t, content[index].GetHardBreak())
		}
		visible, _, _ := richTextSemanticInline(content)
		require.Equal(t, translated, visible)
	}
}

func TestTranslationPlainTargetNewlinesBecomeHardBreaks(t *testing.T) {
	block := paragraphBlock("breaks", &contentv1.RichTextInline{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: "source"}}})
	result := UnitResult{UnitID: "b:typed:paragraph/content", TranslatedText: "\n가\r\n\n나\n"}
	require.NoError(t, ApplyRichTextResults(block, "b", map[string]UnitResult{result.UnitID: result}))
	content := block.GetParagraph().GetContent()
	require.Len(t, content, 6)
	for _, index := range []int{0, 2, 3, 5} {
		require.NotNil(t, content[index].GetHardBreak())
	}
}
