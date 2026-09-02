package contentblock

import (
	"context"
	"errors"
	"testing"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestMaterializeLocalizedRichTextDocumentEscapesTextAndKeepsVariablesLiteral(t *testing.T) {
	blockID := uuid.New()
	document := paragraphDocument(blockID, `<script>{{member.name}}</script>`)
	document.LocaleOverlays[0].Blocks[0].GetParagraph().Content = append(
		document.LocaleOverlays[0].Blocks[0].GetParagraph().Content,
		&contentv1.RichTextInline{Value: &contentv1.RichTextInline_Link{Link: &contentv1.RichTextLink{
			Href:    "https://example.com/?a=1&b=2",
			Content: []*contentv1.RichTextStyledText{{Text: " safe link "}},
		}}},
	)
	localized, err := SnapshotToLocalizedRichTextDocument(snapshotFromRichDocument(t, document), "en")
	require.NoError(t, err)

	result, err := MaterializeLocalizedRichTextDocument(context.Background(), localized, nil)
	require.NoError(t, err)
	require.NotContains(t, result.HTML, "<script>")
	require.Contains(t, result.HTML, "&lt;script&gt;{{member.name}}&lt;/script&gt;")
	require.Contains(t, result.HTML, `href="https://example.com/?a=1&amp;b=2"`)
	require.Equal(t, `<script>{{member.name}}</script> safe link `, result.Text)
}

func TestMaterializeLocalizedRichTextDocumentRendersParagraphPresentationDeterministically(t *testing.T) {
	tests := []struct {
		name         string
		props        func() *contentv1.ParagraphProps
		expectedHTML string
	}{
		{
			name: "center with text and background colors",
			props: func() *contentv1.ParagraphProps {
				alignment := contentv1.ParagraphProps_TEXT_ALIGNMENT_CENTER
				return &contentv1.ParagraphProps{
					TextAlignment:   &alignment,
					TextColor:       stringPointer("#445566"),
					BackgroundColor: stringPointer("#112233"),
				}
			},
			expectedHTML: `<p style="text-align:center;color:#445566;background-color:#112233">body</p>`,
		},
		{
			name: "right alignment",
			props: func() *contentv1.ParagraphProps {
				alignment := contentv1.ParagraphProps_TEXT_ALIGNMENT_RIGHT
				return &contentv1.ParagraphProps{TextAlignment: &alignment}
			},
			expectedHTML: `<p style="text-align:right">body</p>`,
		},
		{
			name: "default and absent values omitted",
			props: func() *contentv1.ParagraphProps {
				alignment := contentv1.ParagraphProps_TEXT_ALIGNMENT_LEFT
				return &contentv1.ParagraphProps{
					TextAlignment: &alignment,
					TextColor:     stringPointer("default"),
				}
			},
			expectedHTML: `<p>body</p>`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := paragraphDocument(uuid.New(), "body")
			document.GetBase().GetNodes()[0].GetBlock().GetParagraph().Props = test.props()
			localized := &contentv1.LocalizedRichTextDocument{
				BlockCatalogFingerprint: document.GetBlockCatalogFingerprint(),
				Profile:                 document.GetProfile(),
				Locale:                  "en",
				Base:                    document.GetBase(),
				LocaleOverlay:           document.GetLocaleOverlays()[0],
			}

			result, err := MaterializeLocalizedRichTextDocument(t.Context(), localized, nil)
			require.NoError(t, err)
			require.Equal(t, test.expectedHTML, result.HTML)
			require.Equal(t, "body", result.Text)
		})
	}
}

func TestMaterializeLocalizedRichTextDocumentRendersCalloutCopyBeforeOptionalChildren(t *testing.T) {
	calloutID := uuid.NewString()
	paragraphID := uuid.NewString()
	parentID := calloutID
	document := &contentv1.LocalizedRichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
		Locale:                  "en",
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{
			{
				Block: &contentv1.RichTextBlock{Id: calloutID, Value: &contentv1.RichTextBlock_Callout{Callout: &contentv1.CalloutBlock{Props: &contentv1.CalloutProps{
					Icon: stringPointer("⚠️"), BackgroundColor: stringPointer("yellow"), TextColor: stringPointer("default"),
				}}}},
				Placement: &contentv1.ContentBlockPlacement{Index: 0},
			},
			{
				Block:     &contentv1.RichTextBlock{Id: paragraphID, Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}}}},
				Placement: &contentv1.ContentBlockPlacement{ParentBlockId: &parentID, Index: 0},
			},
		}},
		LocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "en", Blocks: []*contentv1.RichTextBlockLocale{
			{BlockId: calloutID, Value: &contentv1.RichTextBlockLocale_Callout{Callout: &contentv1.CalloutBlockLocale{Props: &contentv1.CalloutLocaleProps{}, Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: "Clear the rights first."}}}}}}},
			{BlockId: paragraphID, Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{Props: &contentv1.ParagraphLocaleProps{}, Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: "Nested details."}}}}}}},
		}},
	}

	result, err := MaterializeLocalizedRichTextDocument(t.Context(), document, nil)
	require.NoError(t, err)
	require.Equal(t, `<aside data-callout="" data-bg-color="yellow" data-text-color="default"><span data-callout-icon="" aria-hidden="true">⚠️</span><div data-callout-content=""><div data-callout-copy="">Clear the rights first.</div><p>Nested details.</p></div></aside>`, result.HTML)
	require.Equal(t, "Clear the rights first.\nNested details.", result.Text)
}

func TestMaterializeLocalizedRichTextDocumentRejectsInvalidParagraphPresentation(t *testing.T) {
	for _, value := range []string{"", `#112233" onmouseover="alert(1)`} {
		t.Run(value, func(t *testing.T) {
			document := paragraphDocument(uuid.New(), "body")
			document.GetBase().GetNodes()[0].GetBlock().GetParagraph().Props.BackgroundColor =
				stringPointer(value)
			localized := &contentv1.LocalizedRichTextDocument{
				BlockCatalogFingerprint: document.GetBlockCatalogFingerprint(),
				Profile:                 document.GetProfile(),
				Locale:                  "en",
				Base:                    document.GetBase(),
				LocaleOverlay:           document.GetLocaleOverlays()[0],
			}

			_, err := MaterializeLocalizedRichTextDocument(t.Context(), localized, nil)
			require.ErrorIs(t, err, ErrInvalidMutation)
			require.Contains(t, err.Error(), "invalid editor color")
		})
	}
}

func TestMaterializeLocalizedRichTextDocumentRejectsUnsafeLink(t *testing.T) {
	document := paragraphDocument(uuid.New(), "")
	document.LocaleOverlays[0].Blocks[0].GetParagraph().Content = []*contentv1.RichTextInline{{
		Value: &contentv1.RichTextInline_Link{Link: &contentv1.RichTextLink{
			Href: "javascript:alert(1)", Content: []*contentv1.RichTextStyledText{{Text: "click"}},
		}},
	}}
	localized := &contentv1.LocalizedRichTextDocument{
		BlockCatalogFingerprint: document.BlockCatalogFingerprint,
		Profile:                 document.Profile,
		Locale:                  "en",
		Base:                    document.Base,
		LocaleOverlay:           document.LocaleOverlays[0],
	}

	_, err := MaterializeLocalizedRichTextDocument(context.Background(), localized, nil)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrInvalidMutation)
}

func TestMaterializeLocalizedRichTextDocumentEscapesCodeAndTable(t *testing.T) {
	codeID := uuid.New()
	tableID := uuid.New()
	rowID := uuid.New()
	cellID := uuid.New()
	document := &contentv1.LocalizedRichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POLICY,
		Locale:                  "en",
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{
			{Block: &contentv1.RichTextBlock{Id: codeID.String(), Value: &contentv1.RichTextBlock_CodeBlock{CodeBlock: &contentv1.CodeBlockBlock{Props: &contentv1.CodeBlockProps{}}}}, Placement: &contentv1.ContentBlockPlacement{}},
			{Block: &contentv1.RichTextBlock{Id: tableID.String(), Value: &contentv1.RichTextBlock_Table{Table: &contentv1.TableBlock{
				Props:   &contentv1.TableProps{},
				Content: &contentv1.RichTextTableBase{Rows: []*contentv1.RichTextTableRowBase{{Id: rowID.String(), Cells: []*contentv1.RichTextTableCellBase{{Id: cellID.String(), Header: true, Props: &contentv1.RichTextTableCellProps{}}}}}},
			}}}, Placement: &contentv1.ContentBlockPlacement{Index: 1}},
		}},
		LocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "en", Blocks: []*contentv1.RichTextBlockLocale{
			{BlockId: codeID.String(), Value: &contentv1.RichTextBlockLocale_CodeBlock{CodeBlock: &contentv1.CodeBlockBlockLocale{Props: &contentv1.CodeBlockLocaleProps{}, Content: `</code><script>`}}},
			{BlockId: tableID.String(), Value: &contentv1.RichTextBlockLocale_Table{Table: &contentv1.TableBlockLocale{Props: &contentv1.TableLocaleProps{}, Content: &contentv1.RichTextTableLocale{Rows: []*contentv1.RichTextTableRowLocale{{RowId: rowID.String(), Cells: []*contentv1.RichTextTableCellLocale{{CellId: cellID.String(), Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: `<b>cell</b>`}}}}}}}}}}}},
		}},
	}

	result, err := MaterializeLocalizedRichTextDocument(context.Background(), document, nil)
	require.NoError(t, err)
	require.Contains(t, result.HTML, `&lt;/code&gt;&lt;script&gt;`)
	require.Contains(t, result.HTML, `<table><tbody><tr><th>&lt;b&gt;cell&lt;/b&gt;</th></tr></tbody></table>`)
	require.NotContains(t, result.HTML, "<script>")
}

func TestMaterializeLocalizedRichTextDocumentMissingAndActiveFiles(t *testing.T) {
	blockID := uuid.New()
	formerID := uuid.New()
	missing := localizedFileDocument(blockID, &contentv1.FileAttachment{State: &contentv1.FileAttachment_MissingAttachment{
		MissingAttachment: &contentv1.MissingAttachment{FormerFileId: formerID.String(), MediaKind: contentv1.MissingAttachmentMediaKind_MISSING_ATTACHMENT_MEDIA_KIND_FILE},
	}})
	result, err := MaterializeLocalizedRichTextDocument(context.Background(), missing, nil)
	require.NoError(t, err)
	require.Contains(t, result.HTML, `data-missing-attachment="true"`)
	require.Contains(t, result.Text, "gone")

	activeID := uuid.New()
	active := localizedFileDocument(blockID, &contentv1.FileAttachment{State: &contentv1.FileAttachment_ActiveFileId{ActiveFileId: activeID.String()}})
	_, err = MaterializeLocalizedRichTextDocument(context.Background(), active, nil)
	require.ErrorIs(t, err, ErrFileReference)

	resolver := renderResolverFunc(func(context.Context, FileRenderSelector) (FileRenderTarget, error) {
		return FileRenderTarget{URL: "https://cdn.example.com/image.jpg", MIMEType: "image/jpeg"}, nil
	})
	result, err = MaterializeLocalizedRichTextDocument(context.Background(), active, resolver)
	require.NoError(t, err)
	require.Contains(t, result.HTML, `<img src="https://cdn.example.com/image.jpg" alt="alt">`)

	badResolver := renderResolverFunc(func(context.Context, FileRenderSelector) (FileRenderTarget, error) {
		return FileRenderTarget{URL: "javascript:alert(1)", MIMEType: "image/jpeg"}, nil
	})
	_, err = MaterializeLocalizedRichTextDocument(context.Background(), active, badResolver)
	require.ErrorIs(t, err, ErrFileReference)

	failing := renderResolverFunc(func(context.Context, FileRenderSelector) (FileRenderTarget, error) {
		return FileRenderTarget{}, errors.New("not published")
	})
	_, err = MaterializeLocalizedRichTextDocument(context.Background(), active, failing)
	require.ErrorIs(t, err, ErrFileReference)

	placeholderResolver := renderResolverFunc(func(context.Context, FileRenderSelector) (FileRenderTarget, error) {
		return FileRenderTarget{MIMEType: "audio/mpeg"}, nil
	})
	result, err = MaterializeLocalizedRichTextDocument(context.Background(), active, placeholderResolver)
	require.NoError(t, err)
	require.Contains(t, result.HTML, `data-content-block-file="`+activeID.String()+`"`)
	require.NotContains(t, result.HTML, "src=")
}

func TestCompleteRichTextDocumentLocales(t *testing.T) {
	document := paragraphDocument(uuid.New(), "source")
	document.LocaleOverlays = append(document.LocaleOverlays, &contentv1.RichTextLocaleOverlay{Locale: "ko"})
	locales, err := CompleteRichTextDocumentLocales(document)
	require.NoError(t, err)
	require.Equal(t, []string{"en", "ko"}, locales)

	document.LocaleOverlays[0].Blocks = nil
	_, err = CompleteRichTextDocumentLocales(document)
	require.ErrorIs(t, err, ErrInvalidMutation)
}

func TestMaterializeSnapshotLocaleDistinguishesMissingFromExplicitEmpty(t *testing.T) {
	firstID := uuid.New()
	secondID := uuid.New()
	document := paragraphDocument(firstID, "current source")
	second := paragraphDocument(secondID, "new source")
	second.GetBase().GetNodes()[0].GetPlacement().Index = 1
	document.GetBase().Nodes = append(document.GetBase().Nodes, second.GetBase().GetNodes()[0])
	document.GetLocaleOverlays()[0].Blocks = append(
		document.GetLocaleOverlays()[0].Blocks,
		second.GetLocaleOverlays()[0].GetBlocks()[0],
	)
	snapshot := snapshotFromRichDocument(t, document)
	emptyTarget := paragraphDocument(firstID, "")
	targetReplace, err := ReplaceFromRichTextProto(uuid.New(), uuid.New(), emptyTarget)
	require.NoError(t, err)
	snapshot.LocaleOverlays = append(snapshot.LocaleOverlays, LocaleOverlay{
		Locale: "ko",
		Blocks: []LocaleBlockUpdate{targetReplace.LocaleOverlays[0].Blocks[0]},
	})

	localized, err := MaterializeSnapshotRichTextLocale(snapshot, "ko")
	require.NoError(t, err)
	require.Len(t, localized.GetLocaleOverlay().GetBlocks(), 2)
	require.Empty(t, localized.GetLocaleOverlay().GetBlocks()[0].GetParagraph().GetContent()[0].GetText().GetText())
	require.Equal(t, "new source", localized.GetLocaleOverlay().GetBlocks()[1].GetParagraph().GetContent()[0].GetText().GetText())
	result, err := MaterializeLocalizedRichTextDocument(context.Background(), localized, nil)
	require.NoError(t, err)
	require.Equal(t, "new source", result.Text, "explicit empty stays empty while a missing new unit falls back to current source")
}

func TestLocalizedMaterializationFallsBackPerOptionalField(t *testing.T) {
	blockID := uuid.New()
	source := localizedFileDocument(blockID, activeFileAttachment(uuid.New()))
	source.GetLocaleOverlay().GetBlocks()[0].GetFile().Props = &contentv1.FileLocaleProps{
		Alt: stringPointer("source alt"), Caption: stringPointer("source caption"),
	}
	document := &contentv1.RichTextDocument{
		BlockCatalogFingerprint: source.GetBlockCatalogFingerprint(),
		Profile:                 source.GetProfile(),
		SourceLocale:            "en",
		Base:                    source.GetBase(),
		LocaleOverlays: []*contentv1.RichTextLocaleOverlay{
			source.GetLocaleOverlay(),
			{Locale: "ko", Blocks: []*contentv1.RichTextBlockLocale{{
				BlockId: blockID.String(),
				Value: &contentv1.RichTextBlockLocale_File{File: &contentv1.FileBlockLocale{Props: &contentv1.FileLocaleProps{
					Alt: stringPointer(""),
				}}},
			}}},
		},
	}

	localized, err := localizedRichTextDocumentForMaterialization(document, "ko")
	require.NoError(t, err)
	props := localized.GetLocaleOverlay().GetBlocks()[0].GetFile().GetProps()
	require.NotNil(t, props.Alt)
	require.Empty(t, props.GetAlt(), "an explicitly empty target field remains empty")
	require.Equal(t, "source caption", props.GetCaption(), "a missing target field falls back to current source")
}

func TestLocalizedMaterializationFallsBackPerStableTableCell(t *testing.T) {
	blockID := uuid.NewString()
	firstRowID := uuid.NewString()
	newRowID := uuid.NewString()
	firstCellID := uuid.NewString()
	missingCellID := uuid.NewString()
	newCellID := uuid.NewString()
	deletedRowID := uuid.NewString()
	deletedCellID := uuid.NewString()
	inline := func(value string) []*contentv1.RichTextInline {
		return []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: value}}}}
	}
	document := &contentv1.RichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POLICY,
		SourceLocale:            "en",
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
			Block: &contentv1.RichTextBlock{Id: blockID, Value: &contentv1.RichTextBlock_Table{Table: &contentv1.TableBlock{
				Props: &contentv1.TableProps{}, Content: &contentv1.RichTextTableBase{Rows: []*contentv1.RichTextTableRowBase{
					{Id: firstRowID, Cells: []*contentv1.RichTextTableCellBase{{Id: firstCellID, Props: &contentv1.RichTextTableCellProps{}}, {Id: missingCellID, Props: &contentv1.RichTextTableCellProps{}}}},
					{Id: newRowID, Cells: []*contentv1.RichTextTableCellBase{{Id: newCellID, Props: &contentv1.RichTextTableCellProps{}}}},
				}},
			}}}, Placement: &contentv1.ContentBlockPlacement{},
		}}},
		LocaleOverlays: []*contentv1.RichTextLocaleOverlay{
			{Locale: "en", Blocks: []*contentv1.RichTextBlockLocale{{BlockId: blockID, Value: &contentv1.RichTextBlockLocale_Table{Table: &contentv1.TableBlockLocale{
				Props: &contentv1.TableLocaleProps{}, Content: &contentv1.RichTextTableLocale{Rows: []*contentv1.RichTextTableRowLocale{
					{RowId: firstRowID, Cells: []*contentv1.RichTextTableCellLocale{{CellId: firstCellID, Content: inline("source first")}, {CellId: missingCellID, Content: inline("source missing")}}},
					{RowId: newRowID, Cells: []*contentv1.RichTextTableCellLocale{{CellId: newCellID, Content: inline("new source")}}},
				}},
			}}}}},
			{Locale: "ko", Blocks: []*contentv1.RichTextBlockLocale{{BlockId: blockID, Value: &contentv1.RichTextBlockLocale_Table{Table: &contentv1.TableBlockLocale{
				Content: &contentv1.RichTextTableLocale{Rows: []*contentv1.RichTextTableRowLocale{
					{RowId: deletedRowID, Cells: []*contentv1.RichTextTableCellLocale{{CellId: deletedCellID, Content: inline("deleted")}}},
					{RowId: firstRowID, Cells: []*contentv1.RichTextTableCellLocale{{CellId: firstCellID}}},
				}},
			}}}}},
		},
	}

	localized, err := localizedRichTextDocumentForMaterialization(document, "ko")
	require.NoError(t, err)
	rows := localized.GetLocaleOverlay().GetBlocks()[0].GetTable().GetContent().GetRows()
	require.Len(t, rows, 2)
	require.Equal(t, firstRowID, rows[0].GetRowId())
	require.Len(t, rows[0].GetCells(), 2)
	require.Empty(t, rows[0].GetCells()[0].GetContent(), "an explicitly present empty target cell remains empty")
	require.Equal(t, "source missing", rows[0].GetCells()[1].GetContent()[0].GetText().GetText())
	require.Equal(t, newRowID, rows[1].GetRowId(), "a newly added source row remains in the public projection")
	require.Equal(t, "new source", rows[1].GetCells()[0].GetContent()[0].GetText().GetText())

	materialized, err := MaterializeLocalizedRichTextDocument(t.Context(), localized, nil)
	require.NoError(t, err)
	require.Equal(t, "\tsource missing\nnew source", materialized.Text)
	require.NotContains(t, materialized.Text, "deleted")
}

type renderResolverFunc func(context.Context, FileRenderSelector) (FileRenderTarget, error)

func (function renderResolverFunc) ResolveContentBlockFile(ctx context.Context, selector FileRenderSelector) (FileRenderTarget, error) {
	return function(ctx, selector)
}

func localizedFileDocument(blockID uuid.UUID, attachment *contentv1.FileAttachment) *contentv1.LocalizedRichTextDocument {
	return &contentv1.LocalizedRichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
		Locale:                  "en",
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
			Block:     &contentv1.RichTextBlock{Id: blockID.String(), Value: &contentv1.RichTextBlock_File{File: &contentv1.FileBlock{Props: &contentv1.FileProps{Attachment: attachment, Name: stringPointer("file")}}}},
			Placement: &contentv1.ContentBlockPlacement{},
		}}},
		LocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "en", Blocks: []*contentv1.RichTextBlockLocale{{
			BlockId: blockID.String(),
			Value: &contentv1.RichTextBlockLocale_File{File: &contentv1.FileBlockLocale{Props: &contentv1.FileLocaleProps{
				Alt: stringPointer("alt"), Caption: stringPointer("gone"),
			}}},
		}}},
	}
}

func snapshotFromRichDocument(t *testing.T, document *contentv1.RichTextDocument) Snapshot {
	t.Helper()
	replace, err := ReplaceFromRichTextProto(uuid.New(), uuid.New(), document)
	require.NoError(t, err)
	return Snapshot{
		Document:       Document{Profile: "post"},
		SourceLocale:   document.GetSourceLocale(),
		Blocks:         replace.Blocks,
		LocaleOverlays: replace.LocaleOverlays,
	}
}

func stringPointer(value string) *string { return &value }
