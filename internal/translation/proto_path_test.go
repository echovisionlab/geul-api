package translation

import (
	"testing"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

func TestCopyStableProtoPathRequiresSchemaOwnedListIdentity(t *testing.T) {
	t.Parallel()

	title := "translated"
	source := &contentv1.ImmersiveSceneSectionLocale{Units: []*contentv1.PageImmersiveUnitLocale{{
		UnitId: "unit-a", Props: &contentv1.PageImmersiveUnitLocaleProps{Title: &title},
	}}}
	destination := &contentv1.ImmersiveSceneSectionLocale{Units: []*contentv1.PageImmersiveUnitLocale{{
		UnitId: "unit-a", Props: &contentv1.PageImmersiveUnitLocaleProps{},
	}}}
	if err := CopyStableProtoPath(
		destination.ProtoReflect(), source.ProtoReflect(), []string{"units", "0", "props", "title"},
	); err == nil {
		t.Fatal("numeric repeated-field segment was accepted")
	}
	if err := CopyStableProtoPath(
		destination.ProtoReflect(), source.ProtoReflect(), []string{"units", "unit-a", "props", "title"},
	); err != nil {
		t.Fatalf("stable unit_id path rejected: %v", err)
	}
	if got := destination.GetUnits()[0].GetProps().GetTitle(); got != title {
		t.Fatalf("copied title = %q, want %q", got, title)
	}
}

func TestCopyStableProtoPathMatchesStableTableRowAndCell(t *testing.T) {
	t.Parallel()

	source := &contentv1.RichTextTableLocale{Rows: []*contentv1.RichTextTableRowLocale{{
		RowId: "row-a", Cells: []*contentv1.RichTextTableCellLocale{{
			CellId: "cell-a", Content: providerTargetInlineForProtoPath("translated"),
		}},
	}}}
	destination := &contentv1.RichTextTableLocale{Rows: []*contentv1.RichTextTableRowLocale{{
		RowId: "row-a", Cells: []*contentv1.RichTextTableCellLocale{{CellId: "cell-a"}},
	}}}
	if err := CopyStableProtoPath(
		destination.ProtoReflect(), source.ProtoReflect(),
		[]string{"rows", "row-a", "cells", "cell-a", "content"},
	); err != nil {
		t.Fatalf("stable row_id/cell_id path rejected: %v", err)
	}
	if got := destination.GetRows()[0].GetCells()[0].GetContent()[0].GetText().GetText(); got != "translated" {
		t.Fatalf("copied table text = %q", got)
	}
}

func providerTargetInlineForProtoPath(text string) []*contentv1.RichTextInline {
	return []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
		Text: &contentv1.RichTextStyledText{Text: text},
	}}}
}
