package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/echovisionlab/geul-api/internal/email"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestFixtureUsesProductionDocumentAndEmailRendering(t *testing.T) {
	t.Parallel()
	const blockID = "10000000-0000-4000-8000-000000000111"
	document := &contentv1.LocalizedRichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_EMAIL, Locale: "en",
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
			Block: &contentv1.RichTextBlock{Id: blockID, Value: &contentv1.RichTextBlock_Quote{Quote: &contentv1.QuoteBlock{}}}, Placement: &contentv1.ContentBlockPlacement{},
		}}},
		LocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "en", Blocks: []*contentv1.RichTextBlockLocale{{
			BlockId: blockID, Value: &contentv1.RichTextBlockLocale_Quote{Quote: &contentv1.QuoteBlockLocale{Content: []*contentv1.RichTextInline{
				{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: "Quote {{recipient_name}}"}}},
			}}},
		}}},
	}
	documentJSON, err := protojson.Marshal(document)
	require.NoError(t, err)
	variables := map[string]string{"recipient_name": "A & B"}
	layout := `<html><body style="font-family:Georgia"><h1>{{subject}}</h1>{{content}}</body></html>`
	input, err := json.Marshal(draftRequest{Document: documentJSON, Subject: "Subject {{recipient_name}}", LayoutHTML: layout, Variables: variables, Locale: "en"})
	require.NoError(t, err)
	var output bytes.Buffer
	require.NoError(t, renderFixture(t.Context(), bytes.NewReader(input), &output))
	var response draftResponse
	require.NoError(t, json.Unmarshal(output.Bytes(), &response))
	prepared, err := email.PrepareDraftLayoutHTML(layout)
	require.NoError(t, err)
	production, err := email.RenderDraftEmail(t.Context(), email.DraftEmail{Document: document, Subject: "Subject {{recipient_name}}", LayoutHTML: prepared, Variables: variables, Locale: "en"})
	require.NoError(t, err)
	require.Equal(t, production.Subject, response.Subject)
	require.Equal(t, production.HTML, response.HTML)
	require.Equal(t, production.Text, response.Text)
	require.Contains(t, response.HTML, "border-left:2px solid #bbb7aa")
}

func TestFixtureRejectsInvalidInputsWithoutDumpingContent(t *testing.T) {
	t.Parallel()
	for name, input := range map[string]string{
		"unknown envelope field": `{"private-draft-content":"hidden"}`,
		"unknown document field": `{"document":{"private-draft-content":true}}`,
		"ambiguous body":         `{"document":{},"contentHtml":"private-draft-content"}`,
		"trailing object":        `{} {"contentHtml":"private-draft-content"}`,
		"invalid variables":      `{"variables":{"name":false}}`,
		"missing variable":       `{"contentHtml":"<p>{{private-draft-content}}</p>"}`,
		"null envelope":          `null`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			err := renderFixture(t.Context(), strings.NewReader(input), &output)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "private-draft-content")
			require.Empty(t, output.String())
		})
	}
}

func TestFixtureSupportsRawLayoutStoryInput(t *testing.T) {
	t.Parallel()
	input := `{"subject":"Hello","contentHtml":"<blockquote style=\"font-family:Georgia\">Quote</blockquote>","layoutHtml":"<html><body>{{content}}<p>{{subject}}</p></body></html>","variables":{},"locale":"en"}`
	var output bytes.Buffer
	require.NoError(t, renderFixture(t.Context(), strings.NewReader(input), &output))
	var response draftResponse
	require.NoError(t, json.Unmarshal(output.Bytes(), &response))
	require.Equal(t, "Hello", response.Subject)
	require.Contains(t, response.HTML, "border-left:2px solid #bbb7aa")
	require.Contains(t, response.HTML, "font-family:Georgia")
	require.Contains(t, response.HTML, "<p>Hello</p>")
}

func TestFixtureBoundsInputBeforeDecoding(t *testing.T) {
	var output bytes.Buffer
	err := renderFixture(t.Context(), strings.NewReader(strings.Repeat(" ", maxDraftInputBytes+1)), &output)
	require.EqualError(t, err, "email draft input exceeds 32 MiB")
	require.Empty(t, output.String())
}
