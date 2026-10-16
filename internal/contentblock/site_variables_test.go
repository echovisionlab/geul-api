package contentblock

import (
	"html"
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/sitevariable"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestMaterializeSiteVariables(t *testing.T) {
	document := paragraphDocument(uuid.New(), "")
	document.LocaleOverlays[0].Blocks[0].GetParagraph().Content = []*contentv1.RichTextInline{
		{Value: &contentv1.RichTextInline_SiteVariable{SiteVariable: &contentv1.RichTextSiteVariable{Key: "site.title", Styles: &contentv1.RichTextStyle{Bold: proto.Bool(true)}}}},
		{Value: &contentv1.RichTextInline_SiteVariable{SiteVariable: &contentv1.RichTextSiteVariable{Key: "contact.email", Linked: true}}},
	}
	localized, err := MaterializeSnapshotRichTextLocale(snapshotFromRichDocument(t, document), "en")
	require.NoError(t, err)
	before := proto.Clone(localized)
	_, err = MaterializeLocalizedRichTextDocument(t.Context(), localized, nil)
	require.Error(t, err)
	rendered, err := MaterializeLocalizedRichTextDocument(t.Context(), localized, nil, sitevariable.Context{SiteTitle: "Raw & <sector>", ContactEmail: "contact@example.test"})
	require.NoError(t, err)
	require.Equal(t, `<p><strong>Raw &amp; &lt;sector&gt;</strong><a href="mailto:contact@example.test">contact@example.test</a></p>`, rendered.HTML)
	require.Equal(t, "Raw & <sector>contact@example.test", rendered.Text)
	require.True(t, proto.Equal(before, localized))
}

func TestCanonicalContactAtomsUseSettingsWithoutStoredRewrite(t *testing.T) {
	document := paragraphDocument(uuid.New(), "")
	for _, key := range []string{"contact.email", "contact.legalEmail"} {
		document.LocaleOverlays[0].Blocks[0].GetParagraph().Content = append(document.LocaleOverlays[0].Blocks[0].GetParagraph().Content,
			&contentv1.RichTextInline{Value: &contentv1.RichTextInline_SiteVariable{SiteVariable: &contentv1.RichTextSiteVariable{Key: key, Linked: true}}})
	}
	localized, err := MaterializeSnapshotRichTextLocale(snapshotFromRichDocument(t, document), "en")
	require.NoError(t, err)
	before := proto.Clone(localized)
	for _, contact := range []string{"contact@example.test", ""} {
		rendered, err := MaterializeLocalizedRichTextDocument(t.Context(), localized, nil, sitevariable.FromSettings(model.SiteSettings{
			ContactEmail: contact, LegalEmail: "legal@example.test",
		}, ""))
		require.NoError(t, err)
		require.Equal(t, contact+"legal@example.test", rendered.Text)
		require.True(t, proto.Equal(before, localized), "rendering must preserve saved keys")
	}
}

func TestMaterializeDescriptiveContactLink(t *testing.T) {
	document := paragraphDocument(uuid.New(), "")
	document.LocaleOverlays[0].Blocks[0].GetParagraph().Content = []*contentv1.RichTextInline{
		{Value: &contentv1.RichTextInline_Link{Link: &contentv1.RichTextLink{
			Href: "mailto:{{contact.legalEmail}}",
			Content: []*contentv1.RichTextStyledText{
				{Text: "Contact our ", Styles: &contentv1.RichTextStyle{Bold: proto.Bool(true)}},
				{Text: "legal team"},
			},
		}}},
	}
	localized, err := MaterializeSnapshotRichTextLocale(snapshotFromRichDocument(t, document), "en")
	require.NoError(t, err)
	require.True(t, HasSiteVariables(localized), "href-only references must load settings")
	before := proto.Clone(localized)
	_, err = MaterializeLocalizedRichTextDocument(t.Context(), localized, nil)
	require.ErrorContains(t, err, "site variable context is required")
	rendered, err := MaterializeLocalizedRichTextDocument(t.Context(), localized, nil, sitevariable.Context{LegalEmail: "legal@example.test"})
	require.NoError(t, err)
	require.Equal(t, `<p><a href="mailto:legal@example.test"><strong>Contact our </strong>legal team</a></p>`, rendered.HTML)
	require.Equal(t, "Contact our legal team", rendered.Text)
	require.True(t, proto.Equal(before, localized), "materialization must preserve stored tokens")
	updated, err := MaterializeLocalizedRichTextDocument(t.Context(), localized, nil, sitevariable.Context{LegalEmail: "new@example.test"})
	require.NoError(t, err)
	require.Contains(t, updated.HTML, `href="mailto:new@example.test"`)
	_, err = MaterializeLocalizedRichTextDocument(t.Context(), localized, nil, sitevariable.Context{LegalEmail: "legal@example.test\nBcc:other@example.test"})
	require.Error(t, err)
}

func TestMaterializeSiteTextTokensInRunsAndLinkLabels(t *testing.T) {
	document := paragraphDocument(uuid.New(), "")
	label := "{{site.title}} / {{site.url}} / {{site.companyName}} / {{contact.legalEmail}} / {{contact.email}} / {{unknown}}"
	document.LocaleOverlays[0].Blocks[0].GetParagraph().Content = []*contentv1.RichTextInline{
		{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: "{{site.title}} "}}},
		{Value: &contentv1.RichTextInline_Link{Link: &contentv1.RichTextLink{Href: "/contact", Content: []*contentv1.RichTextStyledText{{Text: label}}}}},
	}
	localized, err := MaterializeSnapshotRichTextLocale(snapshotFromRichDocument(t, document), "en")
	require.NoError(t, err)
	before := proto.Clone(localized)
	rendered, err := MaterializeLocalizedRichTextDocument(t.Context(), localized, nil, sitevariable.Context{
		SiteTitle: "Raw & <sector>", SiteURL: "https://example.test", CompanyName: "Company & Co",
		LegalEmail: "legal@example.test", ContactEmail: "contact@example.test",
	})
	require.NoError(t, err)
	expected := "Raw & <sector> / https://example.test / Company & Co / legal@example.test / contact@example.test / {{unknown}}"
	require.Equal(t, "Raw & <sector> "+expected, rendered.Text)
	require.Equal(t, `<p>Raw &amp; &lt;sector&gt; <a href="/contact">`+html.EscapeString(expected)+`</a></p>`, rendered.HTML)
	require.True(t, proto.Equal(before, localized))
}

func TestMaterializeRejectsLegacyContactAtoms(t *testing.T) {
	for _, key := range []string{"contact.supportEmail", "contact.privacyEmail"} {
		document := paragraphDocument(uuid.New(), "")
		localized, err := MaterializeSnapshotRichTextLocale(snapshotFromRichDocument(t, document), "en")
		require.NoError(t, err)
		localized.LocaleOverlay.Blocks[0].GetParagraph().Content = []*contentv1.RichTextInline{
			{Value: &contentv1.RichTextInline_SiteVariable{SiteVariable: &contentv1.RichTextSiteVariable{Key: key, Linked: true}}},
		}
		_, err = MaterializeLocalizedRichTextDocument(t.Context(), localized, nil, sitevariable.Context{ContactEmail: "contact@example.test"})
		require.Error(t, err, key)
	}
}
