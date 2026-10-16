package email

import (
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSiteVariableHrefs(t *testing.T) {
	data := map[string]string{"site.url": "https://www.rawsector.com", "contact.legalEmail": "legal@rawsector.com", "contact.email": "contact@example.test", "site.title": "Raw & <sector>"}
	for _, href := range []string{"{{site.url}}", "{{site.url}}/tools/hwp?x=1#a", "mailto:{{contact.legalEmail}}", "mailto:{{contact.email}}", "/my"} {
		rendered, err := RenderHTMLVarsStrict(`<a href="`+href+`">{{site.title}}</a>`, data)
		require.NoError(t, err, href)
		require.NotContains(t, rendered, `href="/`)
		require.Contains(t, rendered, "Raw &amp; &lt;sector&gt;")
	}
	for _, href := range []string{"mailto:{{contact.supportEmail}}", "mailto:{{contact.privacyEmail}}", "https://evil.test/{{site.url}}", "mailto:{{site.title}}", "{{site.url}}javascript:alert(1)", "//evil.test", "{{site.url}}/\\evil", "javascript:alert(1)"} {
		_, err := RenderHTMLVarsStrict(`<a href="`+href+`">x</a>`, data)
		require.Error(t, err, href)
	}
	data["contact.legalEmail"] = "legal@example.test%0d%0aBcc:evil@example.test"
	_, err := RenderHTMLVarsStrict(`<a href="mailto:{{contact.legalEmail}}">x</a>`, data)
	require.Error(t, err)
}

func TestEmailSnapshotExpandsSettingsOnceAcrossBodyAndLayout(t *testing.T) {
	snapshot := model.JSONFields{
		"source_locale": "en", "subject": "{{site.title}}",
		"content_html":         "<p>{{site.companyName}}</p>",
		"layout_source_locale": "en",
		"layout_translations":  structured.Values{structured.Fields{"locale": "en", "html_content": "<header>{{site.title}}</header>{{content}}"}},
	}
	rendered, err := RenderCampaignSnapshotForLocale(snapshot, "en", map[string]string{"site.title": "rawsector", "site.companyName": "{{site.title}}"})
	require.NoError(t, err)
	require.Equal(t, "rawsector", rendered.Subject)
	require.Equal(t, "<header>rawsector</header><p>{{site.title}}</p>", rendered.HTML)
}
