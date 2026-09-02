package translationadapter

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	core "github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

func TestProjectBlockInterchangeTargetsPreservesExplicitEmptyAndIgnoresDeletedSourceBlock(t *testing.T) {
	firstID, secondID, deletedID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	base := blockInterchangeBase(
		paragraphBase(firstID), paragraphBase(secondID),
	)
	source := blockInterchangeDocument("ko", base,
		paragraphLocale(firstID, "source first"),
		paragraphLocale(secondID, "source second"),
	)
	plan := blockInterchangePlan(t, source, "artist", "artist-a", "en")
	target := blockInterchangeDocument("en", base,
		paragraphLocale(firstID, ""),
		paragraphLocale(deletedID, "stale target"),
	)

	targets, err := projectBlockInterchangeTargets(plan, target)
	if err != nil {
		t.Fatalf("projectBlockInterchangeTargets() error = %v", err)
	}
	first := "block:" + firstID + ":typed:paragraph/content"
	second := "block:" + secondID + ":typed:paragraph/content"
	deleted := "block:" + deletedID + ":typed:paragraph/content"
	if value, ok := targets[first]; !ok || value.TranslatedText != "" {
		t.Fatalf("explicit empty target = (%+v, %v)", value, ok)
	}
	if _, ok := targets[second]; ok {
		t.Fatal("missing target unit was synthesized from source")
	}
	if _, ok := targets[deleted]; ok {
		t.Fatal("deleted source Block remained an interchange target")
	}
}

func TestBuildBlockInterchangePatchPreservesSparseSiblingAndTargetPresentation(t *testing.T) {
	blockID := uuid.NewString()
	base := blockInterchangeBase(&contentv1.RichTextBlock{
		Id: blockID, Value: &contentv1.RichTextBlock_File{File: &contentv1.FileBlock{}},
	})
	source := blockInterchangeDocument("ko", base, &contentv1.RichTextBlockLocale{
		BlockId: blockID,
		Value: &contentv1.RichTextBlockLocale_File{File: &contentv1.FileBlockLocale{Props: &contentv1.FileLocaleProps{
			Alt: stringPointer("source alt"), Caption: stringPointer("source caption"),
		}}},
	})
	plan := blockInterchangePlan(t, source, "label", "label-a", "en")
	current := blockInterchangeDocument("en", base, &contentv1.RichTextBlockLocale{
		BlockId: blockID,
		Value: &contentv1.RichTextBlockLocale_File{File: &contentv1.FileBlockLocale{Props: &contentv1.FileLocaleProps{
			Alt: stringPointer(""),
		}}},
	})
	caption := "block:" + blockID + ":typed:file/props/caption"

	overlay, err := buildBlockInterchangePatch(plan, source, current, map[string]core.UnitResult{
		caption: {UnitID: caption, TranslatedText: "target caption"},
	})
	if err != nil {
		t.Fatalf("buildBlockInterchangePatch() error = %v", err)
	}
	if len(overlay.GetBlocks()) != 1 {
		t.Fatalf("patch Blocks = %d, want 1", len(overlay.GetBlocks()))
	}
	props := overlay.GetBlocks()[0].GetFile().GetProps()
	if props.Alt == nil || props.GetAlt() != "" {
		t.Fatalf("existing explicit-empty alt was not preserved: %+v", props.Alt)
	}
	if props.Caption == nil || props.GetCaption() != "target caption" {
		t.Fatalf("imported caption = %+v", props.Caption)
	}
}

func TestBlockInterchangeTablePatchUsesStableRowAndCellIdentity(t *testing.T) {
	blockID, rowID, firstCellID, secondCellID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	base := blockInterchangeBase(&contentv1.RichTextBlock{
		Id: blockID,
		Value: &contentv1.RichTextBlock_Table{Table: &contentv1.TableBlock{
			Content: &contentv1.RichTextTableBase{Rows: []*contentv1.RichTextTableRowBase{{
				Id: rowID,
				Cells: []*contentv1.RichTextTableCellBase{
					{Id: firstCellID}, {Id: secondCellID},
				},
			}}},
		}},
	})
	source := blockInterchangeDocument("ko", base, tableLocale(blockID, rowID,
		tableCell(firstCellID, "source first"), tableCell(secondCellID, "source second"),
	))
	plan := blockInterchangePlan(t, source, "release", "release-a", "en")
	current := blockInterchangeDocument("en", base, tableLocale(blockID, rowID,
		tableCell(firstCellID, ""),
	))

	projected, err := projectBlockInterchangeTargets(plan, current)
	if err != nil {
		t.Fatalf("projectBlockInterchangeTargets() error = %v", err)
	}
	first := "block:" + blockID + ":typed:table/content/rows/" + rowID + "/cells/" + firstCellID + "/content"
	second := "block:" + blockID + ":typed:table/content/rows/" + rowID + "/cells/" + secondCellID + "/content"
	if value, ok := projected[first]; !ok || value.TranslatedText != "" {
		t.Fatalf("explicit empty first cell = (%+v, %v)", value, ok)
	}
	if _, ok := projected[second]; ok {
		t.Fatal("missing second cell was synthesized")
	}

	overlay, err := buildBlockInterchangePatch(plan, source, current, map[string]core.UnitResult{
		second: {UnitID: second, TranslatedText: "target second"},
	})
	if err != nil {
		t.Fatalf("buildBlockInterchangePatch() error = %v", err)
	}
	cells := overlay.GetBlocks()[0].GetTable().GetContent().GetRows()[0].GetCells()
	if len(cells) != 2 || cells[0].GetCellId() != firstCellID || cells[1].GetCellId() != secondCellID {
		t.Fatalf("patched stable cells = %+v", cells)
	}
	if len(cells[0].GetContent()) != 0 {
		t.Fatal("existing explicit-empty first cell was changed")
	}
	if got := cells[1].GetContent()[0].GetText().GetText(); got != "target second" {
		t.Fatalf("second cell text = %q", got)
	}
}

func TestBuildBlockInterchangePatchRejectsMismatchedGraph(t *testing.T) {
	blockID := uuid.NewString()
	base := blockInterchangeBase(paragraphBase(blockID))
	source := blockInterchangeDocument("ko", base, paragraphLocale(blockID, "source"))
	plan := blockInterchangePlan(t, source, "artist", "artist-a", "en")
	current := blockInterchangeDocument("en", blockInterchangeBase(paragraphBase(uuid.NewString())))
	handle := "block:" + blockID + ":typed:paragraph/content"
	if _, err := buildBlockInterchangePatch(plan, source, current, map[string]core.UnitResult{
		handle: {UnitID: handle, TranslatedText: "target"},
	}); err == nil {
		t.Fatal("mismatched current source graph was accepted")
	}
}

func TestCopyInterchangeProtoPathMatchesImmersiveUnitIdentity(t *testing.T) {
	title, sibling := "translated title", "source sibling"
	source := &contentv1.ImmersiveSceneSectionLocale{Units: []*contentv1.PageImmersiveUnitLocale{
		{UnitId: "unit-a", Props: &contentv1.PageImmersiveUnitLocaleProps{Title: &title, Text: &sibling}},
	}}
	currentSibling := "target sibling"
	destination := &contentv1.ImmersiveSceneSectionLocale{Units: []*contentv1.PageImmersiveUnitLocale{
		{UnitId: "unit-a", Props: &contentv1.PageImmersiveUnitLocaleProps{Text: &currentSibling}},
	}}

	if err := CopyInterchangeProtoPath(
		destination.ProtoReflect(), source.ProtoReflect(), []string{"units", "unit-a", "props", "title"},
	); err != nil {
		t.Fatalf("CopyInterchangeProtoPath() error = %v", err)
	}
	unit := destination.GetUnits()[0]
	if unit.GetProps().GetTitle() != title {
		t.Fatalf("copied title = %q", unit.GetProps().GetTitle())
	}
	if unit.GetProps().GetText() != currentSibling {
		t.Fatalf("untouched sibling = %q", unit.GetProps().GetText())
	}
}

func blockInterchangePlan(t *testing.T, source *contentv1.LocalizedRichTextDocument, entityType, entityID, targetLocale string) *core.ExtractionPlan {
	t.Helper()
	plan, err := core.BuildRichTextExtractionPlan(&model.TranslationJob{
		EntityType: entityType, EntityID: entityID,
		SourceLocale: source.GetLocale(), TargetLocale: targetLocale,
	}, &core.SourceDocument{ContentBlockDocument: source}, core.RichTextDocumentFields{})
	if err != nil {
		t.Fatalf("BuildRichTextExtractionPlan() error = %v", err)
	}
	return plan
}

func blockInterchangeDocument(locale string, base *contentv1.RichTextBlockGraph, blocks ...*contentv1.RichTextBlockLocale) *contentv1.LocalizedRichTextDocument {
	return &contentv1.LocalizedRichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT,
		Locale:                  locale,
		Base:                    proto.Clone(base).(*contentv1.RichTextBlockGraph),
		LocaleOverlay:           &contentv1.RichTextLocaleOverlay{Locale: locale, Blocks: blocks},
	}
}

func blockInterchangeBase(blocks ...*contentv1.RichTextBlock) *contentv1.RichTextBlockGraph {
	graph := &contentv1.RichTextBlockGraph{}
	for index, block := range blocks {
		graph.Nodes = append(graph.Nodes, &contentv1.RichTextBlockNode{
			Block: block, Placement: &contentv1.ContentBlockPlacement{Index: uint32(index)},
		})
	}
	return graph
}

func paragraphBase(id string) *contentv1.RichTextBlock {
	return &contentv1.RichTextBlock{Id: id, Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{}}}
}

func paragraphLocale(id, value string) *contentv1.RichTextBlockLocale {
	return &contentv1.RichTextBlockLocale{
		BlockId: id,
		Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
			Content: richTextInline(value),
		}},
	}
}

func tableLocale(blockID, rowID string, cells ...*contentv1.RichTextTableCellLocale) *contentv1.RichTextBlockLocale {
	return &contentv1.RichTextBlockLocale{
		BlockId: blockID,
		Value: &contentv1.RichTextBlockLocale_Table{Table: &contentv1.TableBlockLocale{
			Content: &contentv1.RichTextTableLocale{Rows: []*contentv1.RichTextTableRowLocale{{
				RowId: rowID, Cells: cells,
			}}},
		}},
	}
}

func tableCell(id, value string) *contentv1.RichTextTableCellLocale {
	return &contentv1.RichTextTableCellLocale{CellId: id, Content: richTextInline(value)}
}

func richTextInline(value string) []*contentv1.RichTextInline {
	if value == "" {
		return nil
	}
	return []*contentv1.RichTextInline{{
		Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: value}},
	}}
}

func stringPointer(value string) *string { return &value }
