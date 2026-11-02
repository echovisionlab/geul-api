package email

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/structured"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func emailPresentationDocument() *contentv1.LocalizedRichTextDocument {
	const quoteID = "10000000-0000-4000-8000-000000000111"
	const headingID = "10000000-0000-4000-8000-000000000112"
	const listID = "10000000-0000-4000-8000-000000000113"
	const numberedID = "10000000-0000-4000-8000-000000000114"
	text := func(value string) []*contentv1.RichTextInline {
		return []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: value}}}}
	}
	quote := text("Quote {{recipient_name}}")
	quote[0].GetText().Styles = &contentv1.RichTextStyle{Bold: proto.Bool(true), Italic: proto.Bool(true)}
	quote = append(quote,
		&contentv1.RichTextInline{Value: &contentv1.RichTextInline_HardBreak{HardBreak: &contentv1.RichTextHardBreak{}}},
		&contentv1.RichTextInline{Value: &contentv1.RichTextInline_Link{Link: &contentv1.RichTextLink{
			Href: "https://example.com/?a=1&b=2", Content: []*contentv1.RichTextStyledText{{Text: "Read more"}},
		}}},
	)
	return &contentv1.LocalizedRichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_EMAIL, Locale: "en",
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{
			{Block: &contentv1.RichTextBlock{Id: quoteID, Value: &contentv1.RichTextBlock_Quote{Quote: &contentv1.QuoteBlock{}}}, Placement: &contentv1.ContentBlockPlacement{Index: 0}},
			{Block: &contentv1.RichTextBlock{Id: headingID, Value: &contentv1.RichTextBlock_Heading{Heading: &contentv1.HeadingBlock{Props: &contentv1.HeadingProps{Level: contentv1.HeadingProps_LEVEL_2.Enum()}}}}, Placement: &contentv1.ContentBlockPlacement{Index: 1}},
			{Block: &contentv1.RichTextBlock{Id: listID, Value: &contentv1.RichTextBlock_BulletListItem{BulletListItem: &contentv1.BulletListItemBlock{}}}, Placement: &contentv1.ContentBlockPlacement{Index: 2}},
			{Block: &contentv1.RichTextBlock{Id: numberedID, Value: &contentv1.RichTextBlock_NumberedListItem{NumberedListItem: &contentv1.NumberedListItemBlock{Props: &contentv1.NumberedListItemProps{Start: proto.Int32(3)}}}}, Placement: &contentv1.ContentBlockPlacement{Index: 3}},
		}},
		LocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "en", Blocks: []*contentv1.RichTextBlockLocale{
			{BlockId: quoteID, Value: &contentv1.RichTextBlockLocale_Quote{Quote: &contentv1.QuoteBlockLocale{Content: quote}}},
			{BlockId: headingID, Value: &contentv1.RichTextBlockLocale_Heading{Heading: &contentv1.HeadingBlockLocale{Content: text("Heading")}}},
			{BlockId: listID, Value: &contentv1.RichTextBlockLocale_BulletListItem{BulletListItem: &contentv1.BulletListItemBlockLocale{Content: text("First item")}}},
			{BlockId: numberedID, Value: &contentv1.RichTextBlockLocale_NumberedListItem{NumberedListItem: &contentv1.NumberedListItemBlockLocale{Content: text("Third item")}}},
		}},
	}
}

func TestDraftTemplateAndDurableDeliveryShareEmailPresentation(t *testing.T) {
	t.Parallel()
	document := emailPresentationDocument()
	before := proto.Clone(document)
	body, err := contentblock.MaterializeLocalizedRichTextDocument(t.Context(), document, nil)
	require.NoError(t, err)
	layout, err := CanonicalizeLayoutSourceMarkers(`<html><head><style>blockquote { font-family: Georgia; }</style></head><body style="font-family:Georgia"><h1>{{subject}}</h1>{{content}}<blockquote id="layout-note">Layout note</blockquote></body></html>`)
	require.NoError(t, err)
	selectedLayout, err := ResolveLayoutLocaleMarkup(layout, nil)
	require.NoError(t, err)
	variables := map[string]string{"recipient_name": "A & B"}
	draft, err := RenderDraftEmail(t.Context(), DraftEmail{Document: document, Subject: "Hello {{recipient_name}}", LayoutHTML: selectedLayout, Variables: variables, Locale: "en"})
	require.NoError(t, err)
	require.True(t, proto.Equal(before, document), "rendering must not modify the draft")
	require.NotContains(t, variables, "subject", "draft rendering must not modify caller variables")

	db := templateRows("Hello {{recipient_name}}", body.HTML, layout)
	template, err := RenderTemplateForLocale(t.Context(), db, DirectTemplateType("template-1"), "en", map[string]string{"recipient_name": "A & B"})
	require.NoError(t, err)
	campaign, err := RenderCampaignSnapshotForLocale(structured.Fields{
		"source_locale": "en", "subject": "Hello {{recipient_name}}", "content_html": body.HTML,
		"layout_source_locale": "en", "layout_translations": []structured.Fields{{"locale": "en", "html_content": selectedLayout}},
	}, "en", map[string]string{"recipient_name": "A & B"})
	require.NoError(t, err)
	for _, delivered := range []*RenderedEmail{template, campaign} {
		require.Equal(t, draft.Subject, delivered.Subject)
		require.Equal(t, draft.HTML, delivered.HTML)
		require.Equal(t, draft.Text, delivered.Text)
	}
	for _, fragment := range []string{
		`border-left:2px solid #bbb7aa`, `padding-left:12px`, `color:#5c594f`,
		`<strong><em>Quote A &amp; B</em></strong><br>`, `href="https://example.com/?a=1&amp;b=2"`,
		`<h2>Heading</h2>`, `<ul><li>First item</li></ul>`, `<ol start="3"><li>Third item</li></ol>`,
		`<body style="font-family:Georgia">`, `<blockquote id="layout-note">Layout note</blockquote>`,
	} {
		require.Contains(t, draft.HTML, fragment)
	}
}

func TestEmailContentPresentationKeepsAuthorStylesAndRawLayoutUntouched(t *testing.T) {
	t.Parallel()
	body := `<blockquote style="border:0;padding:2px;color:#123456;font-family:Georgia;font-size:20px">Quote<br><a href="https://example.com/?a=1&amp;b=2" title="A &quot;B&quot;">Link</a></blockquote>`
	layout := "<html><head><style>blockquote {\n color:purple;\n}</style></head><body>{{content}}<blockquote id=\"layout\">Raw layout\nquote</blockquote></body></html>"
	rendered, err := RenderEmailHTML(body, layout, nil)
	require.NoError(t, err)
	require.Contains(t, rendered, emailQuoteDefaults+`border:0;padding:2px;color:#123456;font-family:Georgia;font-size:20px`)
	require.Contains(t, rendered, `<a href="https://example.com/?a=1&amp;b=2" title="A &quot;B&quot;">Link</a>`)
	require.Contains(t, rendered, "<style>blockquote {\n color:purple;\n}</style>")
	require.Contains(t, rendered, "<blockquote id=\"layout\">Raw layout\nquote</blockquote>")
	normalized := NormalizeEmailContentHTML(body)
	require.Equal(t, normalized, NormalizeEmailContentHTML(normalized))
}

func TestDraftLayoutPreparationPreservesEscapedTextAndAttributes(t *testing.T) {
	t.Parallel()
	layout := `<html><body><a href="https://example.com/?a=1&amp;b=2" title="A &quot;B&quot;">A &amp; B &lt; C</a>{{content}}</body></html>`
	prepared, err := PrepareDraftLayoutHTML(layout)
	require.NoError(t, err)
	require.Equal(t, layout, prepared)
}
