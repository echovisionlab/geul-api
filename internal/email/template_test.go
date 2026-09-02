package email

import (
	"strings"
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
)

func TestRenderTemplateForLocaleRejectsCampaignOwnedLiveRendering(t *testing.T) {
	rendered, err := RenderTemplateForLocale(
		t.Context(), nil, "campaign:campaign-1", "ko", map[string]string{},
	)
	if rendered != nil {
		t.Fatalf("expected no generic Campaign rendering, got %#v", rendered)
	}
	if err == nil || !strings.Contains(err.Error(), "campaign renderer") {
		t.Fatalf("expected Campaign renderer boundary error, got %v", err)
	}
}

func TestNormalizeRenderedHTML_PreservesVisibleEmptyParagraphs(t *testing.T) {
	input := `<p>first</p><p></p><p class="gap">   </p><p>second</p>`

	got := NormalizeRenderedHTML(input)

	if !strings.Contains(got, `<p>&nbsp;</p>`) {
		t.Fatalf("expected normalized html to include visible empty paragraph, got %q", got)
	}

	if !strings.Contains(got, `<p class="gap">&nbsp;</p>`) {
		t.Fatalf("expected normalized html to preserve paragraph attributes, got %q", got)
	}
}

func TestNormalizeRenderedHTML_DeduplicatesMalformedLinkProtocols(t *testing.T) {
	input := `<p><a href="https://https://example.com/verify/abc123">Verify</a></p>`

	got := NormalizeRenderedHTML(input)

	if !strings.Contains(got, `href="https://example.com/verify/abc123"`) {
		t.Fatalf("expected normalized html to keep a single protocol, got %q", got)
	}
	if strings.Contains(got, `href="https://https://`) {
		t.Fatalf("expected duplicate protocol prefix to be removed, got %q", got)
	}
}

func TestNormalizeRenderedHTML_RemovesDuplicateTrailingDoctype(t *testing.T) {
	input := "<!DOCTYPE html><html><body><p>Hello</p></body></html><!DOCTYPE html>"

	got := NormalizeRenderedHTML(input)

	if strings.Count(strings.ToLower(got), "<!doctype html>") != 1 {
		t.Fatalf("expected duplicate doctype to be removed, got %q", got)
	}
	if !strings.HasPrefix(strings.ToLower(got), "<!doctype html>") {
		t.Fatalf("expected leading doctype to be preserved, got %q", got)
	}
}

func TestNormalizeTemplatePlaceholders_CollapsesFormatterWhitespace(t *testing.T) {
	input := `<style>
  body {
    font-family: {
      {
        email_font_family
      }
    }

    !important;
    direction: {
      {
        email_direction
      }
    }

    ;
  }
</style>`

	got := NormalizeTemplatePlaceholders(input)

	if !strings.Contains(got, `font-family: {{email_font_family}} !important;`) {
		t.Fatalf("expected font placeholder to be normalized, got %q", got)
	}
	if !strings.Contains(got, `direction: {{email_direction}};`) {
		t.Fatalf("expected direction placeholder to be normalized, got %q", got)
	}
}

func TestRenderVars_NormalizesBrokenPlaceholdersBeforeReplacement(t *testing.T) {
	input := `<a href="{
  {
    unsubscribe_link
  }
}">{
  {
    recipient_name
  }
}</a>`

	got := RenderVars(input, map[string]string{
		"unsubscribe_link": "https://example.com/unsub",
		"recipient_name":   "John Doe",
	})

	if !strings.Contains(got, `href="https://example.com/unsub"`) {
		t.Fatalf("expected href placeholder to render, got %q", got)
	}
	if !strings.Contains(got, `>John Doe</a>`) {
		t.Fatalf("expected text placeholder to render, got %q", got)
	}
}

func TestStrictRenderersRejectUnknownPlaceholdersAndUseChannelEscaping(t *testing.T) {
	data := map[string]string{"name": `A&B <Admin>`}

	subject, err := RenderSubjectVarsStrict("Hello {{name}}", data)
	if err != nil {
		t.Fatalf("render subject: %v", err)
	}
	if subject != `Hello A&B <Admin>` {
		t.Fatalf("subject used HTML escaping: %q", subject)
	}

	htmlBody, err := RenderHTMLVarsStrict("<p>Hello {{name}}</p>", data)
	if err != nil {
		t.Fatalf("render HTML: %v", err)
	}
	if htmlBody != `<p>Hello A&amp;B &lt;Admin&gt;</p>` {
		t.Fatalf("HTML value was not escaped: %q", htmlBody)
	}

	_, err = RenderHTMLVarsStrict("<p>{{unknown_name}}</p>", data)
	if err == nil || !strings.Contains(err.Error(), "unknown_name") {
		t.Fatalf("unknown placeholder survived strict rendering: %v", err)
	}
}

func TestRenderCampaignSnapshotForLocaleUsesFrozenLocalizedContentAndLayout(t *testing.T) {
	snapshot := model.JSONFields{
		"source_locale":        "en",
		"layout_source_locale": "en",
		"translations": []structured.Fields{
			{
				"locale":       "en",
				"subject":      "Source {{recipient_email}}",
				"content_html": "<p>Source body {{recipient_email}}</p>",
			},
			{
				"locale":       "ko",
				"subject":      "한국어 {{recipient_email}}",
				"content_html": "<p>한국어 본문 {{recipient_email}}</p>",
			},
		},
		"layout_translations": []structured.Fields{
			{
				"locale":       "en",
				"html_content": "<main><h1>{{subject}}</h1>{{content}}</main>",
			},
			{
				"locale":       "ko",
				"html_content": "<main lang=\"ko\"><h1>{{subject}}</h1>{{content}}</main>",
			},
		},
	}

	rendered, err := RenderCampaignSnapshotForLocale(snapshot, "ko", map[string]string{
		"recipient_email": "reader@example.com",
	})
	if err != nil {
		t.Fatalf("expected snapshot render to succeed, got %v", err)
	}

	if rendered.Subject != "한국어 reader@example.com" {
		t.Fatalf("expected localized snapshot subject, got %q", rendered.Subject)
	}
	if !strings.Contains(rendered.HTML, `<main lang="ko">`) {
		t.Fatalf("expected localized snapshot layout, got %q", rendered.HTML)
	}
	if !strings.Contains(rendered.HTML, `한국어 본문 reader@example.com`) {
		t.Fatalf("expected localized snapshot body, got %q", rendered.HTML)
	}
}

func TestRenderCampaignSnapshotForLocaleSupportsJSONBRoundTripRowsAndIgnoresLegacyStatus(t *testing.T) {
	snapshot := model.JSONFields{
		"source_locale": "en",
		"translations": structured.Values{
			structured.Fields{
				"locale":       "en",
				"subject":      "Source {{recipient_email}}",
				"content_html": "<p>Source body {{recipient_email}}</p>",
			},
			structured.Fields{
				"locale":       "fr",
				"status":       "legacy-stale",
				"subject":      "Francais {{recipient_email}}",
				"content_html": "<p>Corps francais {{recipient_email}}</p>",
			},
		},
	}

	rendered, err := RenderCampaignSnapshotForLocale(snapshot, "fr", map[string]string{
		"recipient_email": "reader@example.com",
	})
	if err != nil {
		t.Fatalf("expected JSONB-shaped snapshot render to succeed, got %v", err)
	}

	if rendered.Subject != "Francais reader@example.com" {
		t.Fatalf("expected exact legacy-status snapshot subject, got %q", rendered.Subject)
	}
	if !strings.Contains(rendered.HTML, "Corps francais reader@example.com") {
		t.Fatalf("expected exact legacy-status snapshot body, got %q", rendered.HTML)
	}
	if rendered.ResolvedByFallback {
		t.Fatalf("did not expect fallback marker for an existing requested locale")
	}
}

func TestRenderCampaignSnapshotForLocaleResolvesContentAndLayoutIndependently(t *testing.T) {
	testCases := []struct {
		name                    string
		snapshot                model.JSONFields
		expectedSubject         string
		expectedHTMLFragment    string
		unexpectedHTMLFragment  string
		expectedBodyFragment    string
		expectedTemplateLocale  string
		expectedLayoutLocale    string
		expectedContentFallback bool
		expectedLayoutFallback  bool
	}{
		{
			name: "target content with source layout",
			snapshot: model.JSONFields{
				"source_locale":        "en",
				"layout_source_locale": "en",
				"translations": []structured.Fields{
					{"locale": "en", "subject": "Source", "content_html": "<p>Source body</p>"},
					{"locale": "ko", "subject": "Target", "content_html": "<p>Target body</p>"},
				},
				"layout_translations": []structured.Fields{
					{"locale": "en", "html_content": `<main lang="en">{{content}}</main>`},
				},
			},
			expectedSubject:         "Target",
			expectedHTMLFragment:    `<main lang="en">`,
			expectedBodyFragment:    "Target body",
			expectedTemplateLocale:  "ko",
			expectedLayoutLocale:    "en",
			expectedContentFallback: false,
			expectedLayoutFallback:  true,
		},
		{
			name: "source content with target layout",
			snapshot: model.JSONFields{
				"source_locale":        "en",
				"layout_source_locale": "en",
				"translations": []structured.Fields{
					{"locale": "en", "subject": "Source", "content_html": "<p>Source body</p>"},
				},
				"layout_translations": []structured.Fields{
					{"locale": "en", "html_content": `<main lang="en">{{content}}</main>`},
					{"locale": "ko", "html_content": `<main lang="ko">{{content}}</main>`},
				},
			},
			expectedSubject:         "Source",
			expectedHTMLFragment:    `<main lang="ko">`,
			expectedBodyFragment:    "Source body",
			expectedTemplateLocale:  "en",
			expectedLayoutLocale:    "ko",
			expectedContentFallback: true,
			expectedLayoutFallback:  false,
		},
		{
			name: "empty target layout does not fall back to source layout",
			snapshot: model.JSONFields{
				"source_locale":        "en",
				"layout_source_locale": "en",
				"translations": []structured.Fields{
					{"locale": "en", "subject": "Source", "content_html": "<p>Source body</p>"},
					{"locale": "ko", "subject": "Target", "content_html": "<p>Target body</p>"},
				},
				"layout_translations": []structured.Fields{
					{"locale": "en", "html_content": `<main lang="en">{{content}}</main>`},
					{"locale": "ko", "html_content": ""},
				},
			},
			expectedSubject:         "Target",
			expectedHTMLFragment:    "Target body",
			unexpectedHTMLFragment:  `<main lang="en">`,
			expectedBodyFragment:    "Target body",
			expectedTemplateLocale:  "ko",
			expectedLayoutLocale:    "ko",
			expectedContentFallback: false,
			expectedLayoutFallback:  false,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			rendered, err := RenderCampaignSnapshotForLocale(testCase.snapshot, "ko", map[string]string{})
			if err != nil {
				t.Fatalf("expected snapshot render to succeed, got %v", err)
			}
			if rendered.Subject != testCase.expectedSubject {
				t.Fatalf("expected subject %q, got %q", testCase.expectedSubject, rendered.Subject)
			}
			if !strings.Contains(rendered.HTML, testCase.expectedHTMLFragment) {
				t.Fatalf("expected layout fragment %q, got %q", testCase.expectedHTMLFragment, rendered.HTML)
			}
			if testCase.unexpectedHTMLFragment != "" && strings.Contains(rendered.HTML, testCase.unexpectedHTMLFragment) {
				t.Fatalf("did not expect source layout fragment %q, got %q", testCase.unexpectedHTMLFragment, rendered.HTML)
			}
			if !strings.Contains(rendered.HTML, testCase.expectedBodyFragment) {
				t.Fatalf("expected body fragment %q, got %q", testCase.expectedBodyFragment, rendered.HTML)
			}
			if rendered.TemplateLocale != testCase.expectedTemplateLocale {
				t.Fatalf("expected template locale %q, got %q", testCase.expectedTemplateLocale, rendered.TemplateLocale)
			}
			if rendered.LayoutLocale != testCase.expectedLayoutLocale {
				t.Fatalf("expected layout locale %q, got %q", testCase.expectedLayoutLocale, rendered.LayoutLocale)
			}
			if rendered.ResolvedByFallback != testCase.expectedContentFallback {
				t.Fatalf("expected content fallback %t, got %t", testCase.expectedContentFallback, rendered.ResolvedByFallback)
			}
			if rendered.LayoutUsedFallback != testCase.expectedLayoutFallback {
				t.Fatalf("expected layout fallback %t, got %t", testCase.expectedLayoutFallback, rendered.LayoutUsedFallback)
			}
		})
	}
}

func TestRenderCampaignSnapshotForLocaleDoesNotFillExactTargetFieldsFromSource(t *testing.T) {
	snapshot := model.JSONFields{
		"subject":       "Source",
		"content_html":  "<p>Source body</p>",
		"source_locale": "en",
		"translations": []structured.Fields{
			{"locale": "en", "subject": "Source", "content_html": "<p>Source body</p>"},
			{"locale": "ko", "subject": "Target"},
		},
	}

	rendered, err := RenderCampaignSnapshotForLocale(snapshot, "ko", map[string]string{})
	if err != nil {
		t.Fatalf("expected snapshot render to succeed, got %v", err)
	}
	if rendered.Subject != "Target" {
		t.Fatalf("expected exact target subject, got %q", rendered.Subject)
	}
	if rendered.HTML != "" || rendered.Text != "" {
		t.Fatalf("expected missing target body to stay empty, got HTML %q and text %q", rendered.HTML, rendered.Text)
	}
	if rendered.TemplateLocale != "ko" || rendered.ResolvedByFallback {
		t.Fatalf("expected exact target locale without fallback, got locale %q fallback %t", rendered.TemplateLocale, rendered.ResolvedByFallback)
	}
}
