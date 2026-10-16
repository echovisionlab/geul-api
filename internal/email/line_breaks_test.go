package email

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ses"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/stretchr/testify/require"
)

type lineBreakSESClient struct{ input *ses.SendEmailInput }

func (c *lineBreakSESClient) SendEmail(_ context.Context, input *ses.SendEmailInput, _ ...func(*ses.Options)) (*ses.SendEmailOutput, error) {
	c.input = input
	return &ses.SendEmailOutput{MessageId: aws.String("test-message")}, nil
}

func TestEmailLineBreaksReachSESAndSMTPParts(t *testing.T) {
	html := `<p>First<br>line</p><p>Second paragraph</p>`
	message := &Email{To: "reader@example.test", Subject: "Subject", HTML: html, Text: StripHTML(html)}
	client := &lineBreakSESClient{}
	_, err := newTestSESAdapter(client).Send(t.Context(), message)
	require.NoError(t, err)
	require.Equal(t, html, aws.ToString(client.input.Message.Body.Html.Data))
	require.Equal(t, "First\nline\n\nSecond paragraph", aws.ToString(client.input.Message.Body.Text.Data))

	smtp := &SMTPAdapter{from: "sender@example.test"}
	raw, err := smtp.buildMessage(message)
	require.NoError(t, err)
	parsed, err := mail.ReadMessage(bytes.NewReader(raw))
	require.NoError(t, err)
	mediaType, params, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	require.NoError(t, err)
	require.Equal(t, "multipart/alternative", mediaType)
	reader := multipart.NewReader(parsed.Body, params["boundary"])
	for _, want := range []struct{ kind, content string }{{"text/plain", message.Text}, {"text/html", html}} {
		part, err := reader.NextRawPart()
		require.NoError(t, err)
		kind, _, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
		require.NoError(t, err)
		require.Equal(t, want.kind, kind)
		decoded, err := io.ReadAll(quotedprintable.NewReader(part))
		require.NoError(t, err)
		require.Equal(t, want.content, strings.ReplaceAll(string(decoded), "\r\n", "\n"))
	}
}

func TestStoredEmailPreservesParagraphsAndHardBreaksThroughLayout(t *testing.T) {
	body := "<p>First\nafter break<br>Another line</p><p>Second paragraph</p>"
	layout, err := CanonicalizeLayoutSourceMarkers(`<html><head><title>{{subject}}</title></head><body><section>{{content}}</section><footer>Footer<br>Last line</footer></body></html>`)
	require.NoError(t, err)
	db, mock := layoutMigrationDB(t)
	mock.ExpectQuery(`SELECT \* FROM "email_template"`).WithArgs("template-1", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "layout_id"}).AddRow("template-1", "legacy"))
	mock.ExpectQuery(`SELECT "source_locale" FROM "email_template"`).WithArgs("template-1", 1).
		WillReturnRows(sqlmock.NewRows([]string{"source_locale"}).AddRow("en"))
	mock.ExpectQuery(`SELECT subject, content_html FROM "email_template_translation"`).WithArgs("template-1", "en", 1).
		WillReturnRows(sqlmock.NewRows([]string{"subject", "content_html"}).AddRow("Subject {{login_code}}", body))
	expectLayoutRender(mock, layout, "", "en")
	rendered, err := RenderTemplateForLocale(t.Context(), db, DirectTemplateType("template-1"), "en", map[string]string{"login_code": "789012"})
	require.NoError(t, err)
	require.Equal(t, "Subject 789012", rendered.Subject)
	require.Contains(t, rendered.HTML, `<p>First<br>after break<br>Another line</p><p>Second paragraph</p>`)
	require.Contains(t, rendered.HTML, `<footer>Footer<br>Last line</footer>`)
	require.Equal(t, "First\nafter break\nAnother line\n\nSecond paragraph\n\nFooter\nLast line", rendered.Text)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestLayoutUnsubscribePlaceholderStillRequiresExplicitBulkData(t *testing.T) {
	layout, err := CanonicalizeLayoutSourceMarkers(`<main>{{content}}</main><footer><a href="{{unsubscribe_link}}">Unsubscribe</a></footer>`)
	require.NoError(t, err)
	db, mock := layoutMigrationDB(t)
	expectLayoutRender(mock, layout, "", "en")
	_, _, err = WrapWithLayoutForLocaleStrict(t.Context(), db, "legacy", "en", "<p>System email</p>", map[string]string{"recipient_email": "reader@example.com"})
	require.ErrorContains(t, err, "unknown or missing email placeholders: unsubscribe_link")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCampaignSnapshotPreservesParagraphsAndHardBreaks(t *testing.T) {
	body := `<p>First<br>after break</p><p>Second paragraph</p>`
	rendered, err := RenderCampaignSnapshotForLocale(model.JSONFields{
		"source_locale": "en", "subject": "Campaign", "content_html": body,
		"layout_source_locale": "en",
		"layout_translations":  []structured.Fields{{"locale": "en", "html_content": `<html><head><title>{{subject}}</title></head><body>{{content}}</body></html>`}},
	}, "en", map[string]string{})
	require.NoError(t, err)
	require.Contains(t, rendered.HTML, body)
	require.Equal(t, "First\nafter break\n\nSecond paragraph", rendered.Text)
}

func TestBlockRoomEmailBreaksReachTemplateCampaignAndProvider(t *testing.T) {
	const blockID = "10000000-0000-4000-8000-000000000111"
	document := &contentv1.LocalizedRichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_EMAIL, Locale: "en",
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{Block: &contentv1.RichTextBlock{Id: blockID, Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{}}}, Placement: &contentv1.ContentBlockPlacement{}}}},
		LocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "en", Blocks: []*contentv1.RichTextBlockLocale{{BlockId: blockID, Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{Content: []*contentv1.RichTextInline{
			{Value: &contentv1.RichTextInline_HardBreak{HardBreak: &contentv1.RichTextHardBreak{}}},
			{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: "a"}}},
			{Value: &contentv1.RichTextInline_HardBreak{HardBreak: &contentv1.RichTextHardBreak{}}},
			{Value: &contentv1.RichTextInline_HardBreak{HardBreak: &contentv1.RichTextHardBreak{}}},
			{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: "b"}}},
			{Value: &contentv1.RichTextInline_HardBreak{HardBreak: &contentv1.RichTextHardBreak{}}},
		}}}}}},
	}
	materialized, err := contentblock.MaterializeLocalizedRichTextDocument(t.Context(), document, nil)
	require.NoError(t, err)
	require.Equal(t, "<p><br>a<br><br>b<br></p>", materialized.HTML)
	require.Equal(t, "\na\n\nb\n", materialized.Text)
	layout, err := CanonicalizeLayoutSourceMarkers(`<html><body>{{content}}</body></html>`)
	require.NoError(t, err)
	db, mock := layoutMigrationDB(t)
	mock.ExpectQuery(`SELECT \* FROM "email_template"`).WithArgs("template-1", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "layout_id"}).AddRow("template-1", "legacy"))
	mock.ExpectQuery(`SELECT "source_locale" FROM "email_template"`).WithArgs("template-1", 1).
		WillReturnRows(sqlmock.NewRows([]string{"source_locale"}).AddRow("en"))
	mock.ExpectQuery(`SELECT subject, content_html FROM "email_template_translation"`).WithArgs("template-1", "en", 1).
		WillReturnRows(sqlmock.NewRows([]string{"subject", "content_html"}).AddRow("Breaks", materialized.HTML))
	expectLayoutRender(mock, layout, "", "en")
	template, err := RenderTemplateForLocale(t.Context(), db, DirectTemplateType("template-1"), "en", map[string]string{})
	require.NoError(t, err)
	require.Equal(t, "\na\n\nb\n", template.Text)
	require.NoError(t, mock.ExpectationsWereMet())
	campaign, err := RenderCampaignSnapshotForLocale(model.JSONFields{
		"source_locale": "en", "subject": "Breaks", "content_html": materialized.HTML, "layout_source_locale": "en",
		"layout_translations": []structured.Fields{{"locale": "en", "html_content": layout}},
	}, "en", map[string]string{})
	require.NoError(t, err)
	require.Equal(t, template.Text, campaign.Text)
	for _, rendered := range []*RenderedEmail{template, campaign} {
		client := &lineBreakSESClient{}
		_, err := newTestSESAdapter(client).Send(t.Context(), &Email{To: "reader@example.test", Subject: rendered.Subject, HTML: rendered.HTML, Text: rendered.Text})
		require.NoError(t, err)
		require.Contains(t, aws.ToString(client.input.Message.Body.Html.Data), materialized.HTML)
		require.Equal(t, "\na\n\nb\n", aws.ToString(client.input.Message.Body.Text.Data))
	}
}
