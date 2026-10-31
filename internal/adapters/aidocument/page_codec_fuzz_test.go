package aidocumentadapter

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect"
	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/richtextinline"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const (
	pageCodecFuzzRichSection  = "0198a7c0-0000-7000-8000-000000000001"
	pageCodecFuzzParagraph    = "0198a7c0-0000-7000-8000-000000000002"
	pageCodecFuzzVideoSection = "0198a7c0-0000-7000-8000-000000000003"
	pageCodecFuzzDocument     = "0198a7c0-0000-7000-8000-0000000000d0"
	pageCodecFuzzRevision     = "0198a7c0-0000-7000-8000-0000000000e0"
	pageCodecFuzzContributor  = "0198a7c0-0000-7000-8000-0000000000f0"
)

var pageCodecSeeds = []struct {
	name, text, caption, uri string
}{
	{"plain", "Hello world", "Caption", "https://youtu.be/example"},
	{"line_breaks", "First\nSecond\r\nThird\r", "Line\nbreak", "https://example.com/video"},
	{"empty_values", "", "", "https://example.com/empty"},
	{"edge_whitespace", "  padded  ", " ", "https://example.com/space"},
	{"unicode", "한글 ✓ émoji 🎵", "캡션", "https://example.com/유니코드"},
}

// pageCodecFuzzDocument builds a source-locale Page with one Rich Text section
// (one paragraph) and one external-video section, using fixed identities so
// projections are byte-stable.
func pageCodecFuzzDocumentForTest(t *testing.T, codec *PageCodec, text, caption, uri string) *contentv1.LocalizedPageDocument {
	t.Helper()
	paragraph, paragraphLocale, err := codec.rich.newBlock("paragraph", pageCodecFuzzParagraph)
	require.NoError(t, err)
	paragraph.Placement = &contentv1.ContentBlockPlacement{}
	paragraphLocale.GetParagraph().Content = []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: text}}}}
	return &contentv1.LocalizedPageDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint, Locale: "en",
		Base: &contentv1.PageSectionGraph{Nodes: []*contentv1.PageSectionNode{
			{
				Section: &contentv1.PageSection{Id: pageCodecFuzzRichSection, Settings: &contentv1.PageSectionSettings{}, Value: &contentv1.PageSection_RichText{RichText: &contentv1.RichTextSection{
					Props: &contentv1.RichTextSectionProps{}, Blocks: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{paragraph}},
				}}},
				Placement: &contentv1.PageSectionPlacement{},
			},
			{
				Section: &contentv1.PageSection{Id: pageCodecFuzzVideoSection, Settings: &contentv1.PageSectionSettings{}, Value: &contentv1.PageSection_ExternalVideo{ExternalVideo: &contentv1.ExternalVideoSection{
					Props: &contentv1.ExternalVideoSectionProps{Uri: uri},
				}}},
				Placement: &contentv1.PageSectionPlacement{Index: 1},
			},
		}},
		LocaleOverlay: &contentv1.PageLocaleOverlay{Locale: "en", Sections: []*contentv1.PageSectionLocale{
			{SectionId: pageCodecFuzzRichSection, Value: &contentv1.PageSectionLocale_RichText{RichText: &contentv1.RichTextSectionLocale{
				Props: &contentv1.RichTextSectionLocaleProps{}, Blocks: &contentv1.RichTextLocaleOverlay{Locale: "en", Blocks: []*contentv1.RichTextBlockLocale{paragraphLocale}},
			}}},
			{SectionId: pageCodecFuzzVideoSection, Value: &contentv1.PageSectionLocale_ExternalVideo{ExternalVideo: &contentv1.ExternalVideoSectionLocale{
				Props: &contentv1.ExternalVideoSectionLocaleProps{Caption: &caption},
			}}},
		}},
	}
}

// compilePageRoundTrip compiles operations against document, rebuilds the
// stored document from the resulting batch and projects it again.
func compilePageRoundTrip(
	t *testing.T,
	codec *PageCodec,
	document *contentv1.LocalizedPageDocument,
	operations []core.Operation,
) ([]core.Node, []core.OperationIssue, error) {
	batch, issues, err := codec.Compile(t.Context(),
		uuid.MustParse(pageCodecFuzzDocument), document, core.LocaleRoleSource,
		core.Revision(pageCodecFuzzRevision), uuid.MustParse(pageCodecFuzzContributor), operations,
	)
	if err != nil || len(issues) != 0 {
		return nil, issues, err
	}
	stored, err := contentblock.SnapshotToLocalizedPageDocument(pageSnapshotFromBatch(batch), "en")
	if err != nil {
		return nil, nil, fmt.Errorf("rebuild Page document from batch: %w", err)
	}
	nodes, err := codec.Project(stored)
	if err != nil {
		return nil, nil, fmt.Errorf("project rebuilt Page document: %w", err)
	}
	return nodes, nil, nil
}

func TestPageCodecProjectionGolden(t *testing.T) {
	codec, err := NewPageCodec()
	require.NoError(t, err)
	for _, seed := range pageCodecSeeds {
		t.Run(seed.name, func(t *testing.T) {
			document := pageCodecFuzzDocumentForTest(t, codec, seed.text, seed.caption, seed.uri)
			nodes, err := codec.Project(document)
			require.NoError(t, err)
			roundTrip, issues, err := compilePageRoundTrip(t, codec, document, nil)
			require.NoError(t, err)
			require.Empty(t, issues)
			require.Equal(t, nodes, roundTrip, "compile/project round trip changed the projection")
			assertGoldenJSON(t, "page_codec/"+seed.name+".golden.json", nodes)
		})
	}
}

// FuzzPageCodecRoundTrip checks that a full source compile of a projected
// Page reproduces the same projection, and that values written through
// set-field operations project back as written (inline line breaks become
// hard breaks, as the Rich Text normalizer stores them).
func FuzzPageCodecRoundTrip(f *testing.F) {
	for _, seed := range pageCodecSeeds {
		f.Add(seed.text, seed.caption, seed.uri)
	}
	codec, err := NewPageCodec()
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, text, caption, uri string) {
		document := pageCodecFuzzDocumentForTest(t, codec, "Seed", "Seed caption", "https://example.com/seed")
		nodes, err := codec.Project(document)
		require.NoError(t, err)
		identity := core.DocumentIdentity{Domain: core.DomainPage, Reference: core.DocumentReference(pageCodecFuzzDocument)}
		loaded := core.Document{
			Identity: identity, SourceLocale: "en", Locale: "en", LocaleExists: true,
			DocumentRevision: core.Revision(pageCodecFuzzRevision), Catalog: codec.Catalog(), Nodes: nodes,
		}
		command, validation := core.ValidateLoadedApply(loaded, core.ApplyRequest{
			Protocol: core.ProtocolVersion, Profile: identity.Domain, Document: identity.Reference, Locale: "en",
			ExpectedDocumentRevision: loaded.DocumentRevision,
			Operations: []core.Operation{
				core.SetFieldOperation(pageCodecFuzzParagraph, "content", core.RichText(core.InlineText(text))),
				core.SetNestedFieldOperation(pageCodecFuzzVideoSection, pageSectionLocaleField, []core.FieldPathSegment{core.ObjectPath("props"), core.ObjectPath("caption")}, core.Text(caption)),
				core.SetNestedFieldOperation(pageCodecFuzzVideoSection, pageSectionDataField, []core.FieldPathSegment{core.ObjectPath("props"), core.ObjectPath("uri")}, core.Text(uri)),
			},
		})
		if !validation.Valid() {
			return
		}
		written, issues, err := compilePageRoundTrip(t, codec, document, command.Operations)
		if err != nil {
			if connect.CodeOf(err) == connect.CodeInvalidArgument {
				return
			}
			t.Fatalf("compile accepted operations: %v", err)
		}
		if len(issues) != 0 {
			return
		}
		paragraph := pageCodecFuzzNode(t, written, pageCodecFuzzParagraph)
		content, ok := fieldValue(paragraph.Localized, "content")
		require.True(t, ok, "paragraph content is missing after round trip")
		if got, want := pageCodecFuzzInlineText(content.Inline), richtextinline.NormalizeNewlines(text); got != want {
			t.Fatalf("paragraph content = %q, want %q", got, want)
		}
		video := pageCodecFuzzNode(t, written, pageCodecFuzzVideoSection)
		localeData, ok := fieldValue(video.Localized, pageSectionLocaleField)
		require.True(t, ok)
		if got := pageTestObjectField(t, pageTestObjectField(t, localeData.Object, "props").Object, "caption"); !reflect.DeepEqual(got, core.Text(caption)) {
			t.Fatalf("caption = %+v, want %q", got, caption)
		}
		data, ok := fieldValue(video.Shared, pageSectionDataField)
		require.True(t, ok)
		if got := pageTestObjectField(t, pageTestObjectField(t, data.Object, "props").Object, "uri"); !reflect.DeepEqual(got, core.Text(uri)) {
			t.Fatalf("uri = %+v, want %q", got, uri)
		}

		// The rebuilt document is itself a fixed point of a full compile.
		stored := pageCodecFuzzRebuild(t, codec, document, command.Operations)
		again, issues, err := compilePageRoundTrip(t, codec, stored, nil)
		require.NoError(t, err)
		require.Empty(t, issues)
		require.Equal(t, written, again, "second compile/project round trip changed the projection")
	})
}

func pageCodecFuzzRebuild(t *testing.T, codec *PageCodec, document *contentv1.LocalizedPageDocument, operations []core.Operation) *contentv1.LocalizedPageDocument {
	t.Helper()
	batch, issues, err := codec.Compile(t.Context(),
		uuid.MustParse(pageCodecFuzzDocument), document, core.LocaleRoleSource,
		core.Revision(pageCodecFuzzRevision), uuid.MustParse(pageCodecFuzzContributor), operations,
	)
	require.NoError(t, err)
	require.Empty(t, issues)
	stored, err := contentblock.SnapshotToLocalizedPageDocument(pageSnapshotFromBatch(batch), "en")
	require.NoError(t, err)
	return stored
}

func pageCodecFuzzNode(t *testing.T, nodes []core.Node, id core.BlockID) core.Node {
	t.Helper()
	for _, node := range nodes {
		if node.ID == id {
			return node
		}
	}
	t.Fatalf("node %s is missing after round trip", id)
	return core.Node{}
}

func pageCodecFuzzInlineText(items []core.InlineItem) string {
	var text strings.Builder
	for _, item := range items {
		switch item.Kind {
		case core.InlineKindHardBreak:
			text.WriteString("\n")
		default:
			text.WriteString(item.Text)
			text.WriteString(pageCodecFuzzInlineText(item.Children))
		}
	}
	return text.String()
}
