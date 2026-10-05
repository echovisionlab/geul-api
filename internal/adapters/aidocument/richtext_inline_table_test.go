package aidocumentadapter

import (
	"testing"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
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

func TestRichTextCodecNestedTableTargetSetCreatesOnlySelectedLocaleCell(t *testing.T) {
	for _, test := range []struct {
		name      string
		state     string
		emptySet  bool
		wantText  string
		wantCells int
	}{
		{"absent block", "block", false, "changed\tsecond target", 1},
		{"absent table content", "content", false, "changed\tsecond target", 1},
		{"absent row", "row", false, "changed\tsecond target", 1},
		{"sparse cells preserve explicit empty sibling", "cell", false, "changed\t", 2},
		{"explicit empty selected cell", "block", true, "\tsecond target", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			codec, source, block, row, cell := localizedTableDocumentForTest(t)
			source.Locale, source.LocaleOverlay.Locale = "en", "en"
			seed, issues, err := codec.Compile(uuid.New(), source, core.LocaleRoleSource, core.Revision(uuid.NewString()), uuid.New(), nil)
			require.NoError(t, err)
			require.Empty(t, issues)
			target := proto.Clone(source).(*contentv1.LocalizedRichTextDocument)
			target.Locale, target.LocaleOverlay.Locale = "ko", "ko"
			switch test.state {
			case "block":
				target.LocaleOverlay.Blocks = nil
			case "content":
				target.LocaleOverlay.Blocks[0].GetTable().Content = nil
			case "row":
				target.LocaleOverlay.Blocks[0].GetTable().Content.Rows = nil
			case "cell":
				cells := target.LocaleOverlay.Blocks[0].GetTable().Content.Rows[0].Cells
				cells[1].Content = nil
				target.LocaleOverlay.Blocks[0].GetTable().Content.Rows[0].Cells = cells[1:]
			}
			initialTarget, issues, err := codec.Compile(seed.DocumentID, target, core.LocaleRoleNonSource, core.Revision(seed.ExpectedRevision.String()), uuid.New(), nil)
			require.NoError(t, err)
			require.Empty(t, issues)
			snapshot := contentblock.Snapshot{Document: contentblock.Document{ID: seed.DocumentID, Profile: "post", Revision: seed.ExpectedRevision}, SourceLocale: "en", Blocks: seed.Upserts, LocaleOverlays: []contentblock.LocaleOverlay{{Locale: "en", Blocks: seed.LocaleGroups[0].Upserts}}}
			if len(initialTarget.LocaleGroups) != 0 {
				snapshot.LocaleOverlays = append(snapshot.LocaleOverlays, contentblock.LocaleOverlay{Locale: "ko", Blocks: initialTarget.LocaleGroups[0].Upserts})
			}
			target, err = contentblock.SnapshotToLocalizedRichTextDocument(snapshot, "ko")
			require.NoError(t, err)
			path := []core.FieldPathSegment{core.ObjectPath("rows"), core.ListPath(core.RelationItemID(row)), core.ObjectPath("cells"), core.ListPath(core.RelationItemID(cell)), core.ObjectPath("content")}
			value := core.RichText(core.InlineText("changed"))
			if test.emptySet {
				value = core.RichText()
			}
			operation := core.SetNestedFieldOperation(core.BlockID(block), richTextTableLocaleField, path, value)
			validateRichTextOperationForTest(t, codec, target, operation)
			before := proto.Clone(target)
			batch, issues, err := codec.Compile(seed.DocumentID, target, core.LocaleRoleNonSource, core.Revision(seed.ExpectedRevision.String()), uuid.New(), []core.Operation{operation})
			require.NoError(t, err)
			require.Empty(t, issues)
			require.True(t, proto.Equal(before, target))
			require.Empty(t, batch.Upserts)
			snapshot.LocaleOverlays = []contentblock.LocaleOverlay{snapshot.LocaleOverlays[0], {Locale: "ko", Blocks: batch.LocaleGroups[0].Upserts}}
			stored, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, "ko")
			require.NoError(t, err)
			rows := stored.LocaleOverlay.Blocks[0].GetTable().Content.Rows
			require.Len(t, rows, 1)
			require.Len(t, rows[0].Cells, test.wantCells)
			loaded, err := contentblock.MaterializeSnapshotRichTextLocale(snapshot, "ko")
			require.NoError(t, err)
			rendered, err := contentblock.MaterializeLocalizedRichTextDocument(t.Context(), loaded, nil)
			require.NoError(t, err)
			require.Equal(t, test.wantText, rendered.Text)
		})
	}
}

func localizedTableDocumentForTest(t *testing.T) (*RichTextCodec, *contentv1.LocalizedRichTextDocument, string, string, string) {
	t.Helper()
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST)
	require.NoError(t, err)
	block, row, first, second := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	text := func(s string) []*contentv1.RichTextInline {
		return []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: s}}}}
	}
	document := &contentv1.LocalizedRichTextDocument{BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint, Profile: contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST, Locale: "ko",
		Base:          &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{Block: &contentv1.RichTextBlock{Id: block, Value: &contentv1.RichTextBlock_Table{Table: &contentv1.TableBlock{Props: &contentv1.TableProps{}, Content: &contentv1.RichTextTableBase{Rows: []*contentv1.RichTextTableRowBase{{Id: row, Cells: []*contentv1.RichTextTableCellBase{{Id: first, Props: &contentv1.RichTextTableCellProps{}}, {Id: second, Props: &contentv1.RichTextTableCellProps{}}}}}}}}}, Placement: &contentv1.ContentBlockPlacement{}}}},
		LocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "ko", Blocks: []*contentv1.RichTextBlockLocale{{BlockId: block, Value: &contentv1.RichTextBlockLocale_Table{Table: &contentv1.TableBlockLocale{Props: &contentv1.TableLocaleProps{}, Content: &contentv1.RichTextTableLocale{Rows: []*contentv1.RichTextTableRowLocale{{RowId: row, Cells: []*contentv1.RichTextTableCellLocale{{CellId: first, Content: text("first target")}, {CellId: second, Content: text("second target")}}}}}}}}}}}
	return codec, document, block, row, first
}

func validateRichTextOperationForTest(t *testing.T, codec *RichTextCodec, wire *contentv1.LocalizedRichTextDocument, op core.Operation) {
	t.Helper()
	nodes, err := codec.Project(wire)
	require.NoError(t, err)
	identity := core.DocumentIdentity{Domain: core.DomainPost, Reference: core.DocumentReference(uuid.NewString())}
	revision := core.Revision(uuid.NewString())
	doc := core.Document{Identity: identity, DocumentRevision: revision, TargetRevision: &revision, SourceLocale: "en", Locale: "ko", LocaleExists: true, Catalog: codec.Catalog(), Nodes: nodes}
	request := core.ApplyRequest{Protocol: core.ProtocolVersion, Profile: identity.Domain, Document: identity.Reference, Locale: "ko", ExpectedDocumentRevision: revision, ExpectedTargetRevision: &revision, Operations: []core.Operation{op}}
	if wire.Locale == "en" {
		doc.Locale = "en"
		doc.TargetRevision = nil
		request.Locale = "en"
		request.ExpectedTargetRevision = nil
	}
	validation := core.ValidateOperations(doc, request)
	require.True(t, validation.Valid(), "generic validation: %+v", validation)
}

func TestRichTextCodecNestedTableCellOperationsPreserveOtherValues(t *testing.T) {
	for _, test := range []struct {
		name     string
		source   bool
		unset    bool
		wantText string
	}{
		{"target set", false, false, "changed\tsecond target"},
		{"source set", true, false, "changed\tsecond target"},
		{"source unset", true, true, "\tsecond target"},
	} {
		t.Run(test.name, func(t *testing.T) {
			codec, document, block, row, cell := localizedTableDocumentForTest(t)
			role := core.LocaleRoleNonSource
			if test.source {
				document.Locale = "en"
				document.LocaleOverlay.Locale = "en"
				role = core.LocaleRoleSource
			}
			path := []core.FieldPathSegment{core.ObjectPath("rows"), core.ListPath(core.RelationItemID(row)), core.ObjectPath("cells"), core.ListPath(core.RelationItemID(cell)), core.ObjectPath("content")}
			operation := core.SetNestedFieldOperation(core.BlockID(block), richTextTableLocaleField, path, core.RichText(core.InlineText("changed")))
			if test.unset {
				operation = core.UnsetNestedFieldOperation(core.BlockID(block), richTextTableLocaleField, path)
			}
			validateRichTextOperationForTest(t, codec, document, operation)
			before := proto.Clone(document)
			batch, issues, err := codec.Compile(uuid.New(), document, role, core.Revision(uuid.NewString()), uuid.New(), []core.Operation{operation})
			require.NoError(t, err)
			require.Empty(t, issues)
			require.True(t, proto.Equal(before, document))
			working := proto.Clone(document).(*contentv1.LocalizedRichTextDocument)
			require.NoError(t, codec.applyOperation(working, operation, map[string]struct{}{}))
			localized := working.LocaleOverlay.Blocks[0].GetTable().Content
			require.Len(t, localized.Rows, 1)
			require.Len(t, localized.Rows[0].Cells, 2)
			require.Equal(t, "second target", localized.Rows[0].Cells[1].Content[0].GetText().Text)
			if test.source {
				snapshot := contentblock.Snapshot{Document: contentblock.Document{ID: batch.DocumentID, Profile: "post", Revision: batch.ExpectedRevision}, SourceLocale: "en", Blocks: batch.Upserts, LocaleOverlays: []contentblock.LocaleOverlay{{Locale: "en", Blocks: batch.LocaleGroups[0].Upserts}}}
				loaded, err := contentblock.MaterializeSnapshotRichTextLocale(snapshot, "en")
				require.NoError(t, err)
				rendered, err := contentblock.MaterializeLocalizedRichTextDocument(t.Context(), loaded, nil)
				require.NoError(t, err)
				require.Equal(t, test.wantText, rendered.Text)
			} else {
				require.Empty(t, batch.Upserts)
				rendered, err := contentblock.MaterializeLocalizedRichTextDocument(t.Context(), working, nil)
				require.NoError(t, err)
				require.Equal(t, test.wantText, rendered.Text)
			}
		})
	}
}

func TestRichTextCodecNestedTableSharedCellLeafAndFailedBatch(t *testing.T) {
	codec, document, block, row, cell := localizedTableDocumentForTest(t)
	document.Locale = "en"
	document.LocaleOverlay.Locale = "en"
	path := []core.FieldPathSegment{core.ObjectPath("rows"), core.ListPath(core.RelationItemID(row)), core.ObjectPath("cells"), core.ListPath(core.RelationItemID(cell)), core.ObjectPath("header")}
	set := core.SetNestedFieldOperation(core.BlockID(block), richTextTableField, path, core.Boolean(true))
	before := proto.Clone(document)
	batch, issues, err := codec.Compile(uuid.New(), document, core.LocaleRoleSource, core.Revision(uuid.NewString()), uuid.New(), []core.Operation{set})
	require.NoError(t, err)
	require.Empty(t, issues)
	require.True(t, proto.Equal(before, document))
	snapshot := contentblock.Snapshot{Document: contentblock.Document{ID: batch.DocumentID, Profile: "post", Revision: batch.ExpectedRevision}, SourceLocale: "en", Blocks: batch.Upserts, LocaleOverlays: []contentblock.LocaleOverlay{{Locale: "en", Blocks: batch.LocaleGroups[0].Upserts}}}
	loaded, err := contentblock.MaterializeSnapshotRichTextLocale(snapshot, "en")
	require.NoError(t, err)
	cells := loaded.Base.Nodes[0].Block.GetTable().Content.Rows[0].Cells
	require.True(t, cells[0].Header)
	require.False(t, cells[1].Header)
	rendered, err := contentblock.MaterializeLocalizedRichTextDocument(t.Context(), loaded, nil)
	require.NoError(t, err)
	require.Equal(t, "first target\tsecond target", rendered.Text)
	missing := append([]core.FieldPathSegment(nil), path...)
	missing[3] = core.ListPath(core.RelationItemID(uuid.NewString()))
	_, issues, err = codec.Compile(uuid.New(), document, core.LocaleRoleSource, core.Revision(uuid.NewString()), uuid.New(), []core.Operation{set, core.SetNestedFieldOperation(core.BlockID(block), richTextTableField, missing, core.Boolean(true))})
	require.NoError(t, err)
	require.Len(t, issues, 1)
	require.Equal(t, 1, issues[0].Operation)
	require.True(t, proto.Equal(before, document))
}
