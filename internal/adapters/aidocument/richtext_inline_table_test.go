package aidocumentadapter

import (
	"testing"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
)

func TestRichTextCodecPreservesStableTableRowsAndCells(t *testing.T) {
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST)
	if err != nil {
		t.Fatal(err)
	}
	blockID, rowID, cellID := uuid.New(), uuid.New(), uuid.New()
	document := &contentv1.LocalizedRichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST, Locale: "en",
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
			Block: &contentv1.RichTextBlock{Id: blockID.String(), Value: &contentv1.RichTextBlock_Table{Table: &contentv1.TableBlock{
				Props: &contentv1.TableProps{}, Content: &contentv1.RichTextTableBase{Rows: []*contentv1.RichTextTableRowBase{{
					Id: rowID.String(), Cells: []*contentv1.RichTextTableCellBase{{Id: cellID.String(), Props: &contentv1.RichTextTableCellProps{}}},
				}}},
			}}}, Placement: &contentv1.ContentBlockPlacement{},
		}}},
		LocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "en", Blocks: []*contentv1.RichTextBlockLocale{{
			BlockId: blockID.String(), Value: &contentv1.RichTextBlockLocale_Table{Table: &contentv1.TableBlockLocale{
				Props: &contentv1.TableLocaleProps{}, Content: &contentv1.RichTextTableLocale{Rows: []*contentv1.RichTextTableRowLocale{{
					RowId: rowID.String(), Cells: []*contentv1.RichTextTableCellLocale{{CellId: cellID.String(), Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: "cell"}}}}}},
				}}},
			}},
		}}},
	}
	nodes, err := codec.Project(document)
	if err != nil {
		t.Fatal(err)
	}
	shared, ok := fieldValue(nodes[0].Shared, richTextTableField)
	if !ok {
		t.Fatal("shared table was not projected")
	}
	rows, _ := coreObjectValue(shared, richTextTableRowsField)
	if len(rows.List) != 1 || string(rows.List[0].ID) != rowID.String() {
		t.Fatalf("shared rows = %+v", rows)
	}
	localized, ok := fieldValue(nodes[0].Localized, richTextTableLocaleField)
	if !ok {
		t.Fatal("locale table was not projected")
	}
	operation := core.SetFieldOperation(core.BlockID(blockID.String()), richTextTableLocaleField, localized)
	batch, issues, err := codec.Compile(uuid.New(), document, core.LocaleRoleNonSource, core.Revision(uuid.NewString()), uuid.New(), []core.Operation{operation})
	if err != nil || len(issues) != 0 || len(batch.LocaleGroups) != 1 {
		t.Fatalf("table compile = (%+v, %+v, %v)", batch, issues, err)
	}
}
