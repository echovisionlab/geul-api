package sitevariablemigrate

import (
	"encoding/json"
	"testing"

	"github.com/echovisionlab/geul-api/internal/email"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/stretchr/testify/require"
)

func TestEmailHrefMigrationRendersAndPreservesMailtoParameters(t *testing.T) {
	data := map[string]string{"site.title": "Rawsector", "site.url": "https://www.rawsector.com"}
	for _, name := range []string{"legal", "privacy", "support"} {
		data["contact."+name+"Email"] = name + "@settings.example"
		for _, fixture := range []struct{ href, want, rendered string }{
			{"mailto:" + name + "@dsub.io", "mailto:{{contact." + name + "Email}}", "mailto:" + name + "@settings.example"},
			{"mailto:" + name + "@dsub.io?subject=Help", "mailto:" + name + "@rawsector.com?subject=Help", "mailto:" + name + "@rawsector.com?subject=Help"},
			{"mailto:" + name + "@dsub.io?subject=Help&amp;cc=support@dsub.io", "mailto:" + name + "@rawsector.com?subject=Help&amp;cc=support@rawsector.com", "mailto:" + name + "@rawsector.com?subject=Help&amp;cc=support@rawsector.com"},
			{"mailto:" + name + "@dsub.io,privacy@dsub.io", "mailto:" + name + "@rawsector.com,privacy@rawsector.com", "mailto:" + name + "@rawsector.com,privacy@rawsector.com"},
			{"mailto:" + name + "@dsub.io.evil?subject=Help", "mailto:" + name + "@dsub.io.evil?subject=Help", "mailto:" + name + "@dsub.io.evil?subject=Help"},
		} {
			for _, attribute := range []string{`href="` + fixture.href + `"`, `HREF='` + fixture.href + `'`, `href=` + fixture.href} {
				t.Run(attribute, func(t *testing.T) {
					got := EmailSource(`<a ` + attribute + `>DSUB ` + name + `@dsub.io</a>`)
					require.Contains(t, got, fixture.want)
					require.Contains(t, got, `>{{site.title}} {{contact.`+name+`Email}}</a>`)
					require.Equal(t, got, EmailSource(got))
					rendered, err := email.RenderHTMLVarsStrict(got, data)
					require.NoError(t, err)
					require.Contains(t, rendered, `href="`+fixture.rendered+`"`)
					require.NotContains(t, rendered, "{{")
				})
			}
		}
	}
	// Attributes, comments, and private layout markers are not prose.
	unchanged := `<a title=' href="mailto:legal@dsub.io?subject=Help"' data-href="mailto:legal@dsub.io">Other</a><!-- DSUB -->`
	require.Equal(t, unchanged, EmailSource(unchanged))
}

func TestTextFixtures(t *testing.T) {
	for _, fixture := range []struct{ source, want string }{
		{"DSUB DSUB.IO DSUB.", "{{site.title}} {{site.title}} {{site.title}}."},
		{"legal@dsub.io privacy@dsub.io support@dsub.io", "{{contact.legalEmail}} {{contact.privacyEmail}} {{contact.supportEmail}}"},
		{"MYDSUB DSUBBY https://example.test/DSUB aDSUB@example.test DSUB@example.test", "MYDSUB DSUBBY https://example.test/DSUB aDSUB@example.test DSUB@example.test"},
		{"/DSUB example.test/DSUB?x=1", "/DSUB example.test/DSUB?x=1"},
		{"https://DSUB.IO/test https://www.dsub.io/DSUB?x=DSUB support@DSUB.IO", "https://DSUB.IO/test https://www.dsub.io/DSUB?x=DSUB support@DSUB.IO"},
		{"https://opendiscogs.dsub.io/docs?q=1#a", "https://opendiscogs.rawsector.com/docs?q=1#a"},
	} {
		t.Run(fixture.source, func(t *testing.T) {
			got := Text(fixture.source)
			require.Equal(t, fixture.want, got)
			require.Equal(t, got, Text(got))
		})
	}
}
func TestRichJSONFixtures(t *testing.T) {
	for _, fixture := range []struct{ name, source, want string }{
		{"styled brand", `{"content":[{"text":{"text":"Hello DSUB!","styles":{"bold":true}}}]}`, `{"content":[{"text":{"text":"Hello ","styles":{"bold":true}}},{"siteVariable":{"key":"site.title","styles":{"bold":true}}},{"text":{"text":"!","styles":{"bold":true}}}]}`},
		{"plain email", `{"content":[{"text":{"text":"support@dsub.io"}}]}`, `{"content":[{"siteVariable":{"key":"contact.supportEmail"}}]}`},
		{"linked email", `{"content":[{"link":{"href":"mailto:legal@dsub.io","content":[{"text":"legal@dsub.io"}]}}]}`, `{"content":[{"siteVariable":{"key":"contact.legalEmail","linked":true}}]}`},
		{"relative path", `{"content":[{"link":{"href":"https://www.dsub.io/tools/hwp?x=1#a","content":[{"text":"HWP"}]}}]}`, `{"content":[{"link":{"href":"/tools/hwp?x=1#a","content":[{"text":"HWP"}]}}]}`},
		{"old url label", `{"content":[{"link":{"href":"https://dsub.io/path?q=1#a","content":[{"text":"https://dsub.io/path?q=1#a"}]}}]}`, `{"content":[{"link":{"href":"/path?q=1#a","content":[{"text":"{{site.url}}/path?q=1#a"}]}}]}`},
		{"service link", `{"content":[{"link":{"href":"https://opendiscogs.dsub.io/docs","content":[{"text":"https://opendiscogs.dsub.io/docs"}]}}]}`, `{"content":[{"link":{"href":"https://opendiscogs.rawsector.com/docs","content":[{"text":"https://opendiscogs.rawsector.com/docs"}]}}]}`},
		{"protected URL email", `{"content":[{"text":{"text":"https://example.test/DSUB DSUB@example.com"}}]}`, `{"content":[{"text":{"text":"https://example.test/DSUB DSUB@example.com"}}]}`},
		{"word prefix in previous run", `{"content":[{"text":{"text":"MY"}},{"text":{"text":"DSUB","styles":{"bold":true}}}]}`, `{"content":[{"text":{"text":"MY"}},{"text":{"text":"DSUB","styles":{"bold":true}}}]}`},
		{"word suffix in next run", `{"content":[{"text":{"text":"DSUB","styles":{"bold":true}}},{"text":{"text":"BY"}}]}`, `{"content":[{"text":{"text":"DSUB","styles":{"bold":true}}},{"text":{"text":"BY"}}]}`},
		{"URL prefix in previous run", `{"content":[{"text":{"text":"https://example.test/"}},{"text":{"text":"DSUB","styles":{"bold":true}}}]}`, `{"content":[{"text":{"text":"https://example.test/"}},{"text":{"text":"DSUB","styles":{"bold":true}}}]}`},
		{"email suffix in next run", `{"content":[{"text":{"text":"DSUB","styles":{"bold":true}}},{"text":{"text":"@example.test"}}]}`, `{"content":[{"text":{"text":"DSUB","styles":{"bold":true}}},{"text":{"text":"@example.test"}}]}`},
		{"matched run style", `{"content":[{"text":{"text":"Hi "}},{"text":{"text":"DSUB","styles":{"bold":true}}},{"text":{"text":"!","styles":{"italic":true}}}]}`, `{"content":[{"text":{"text":"Hi "}},{"siteVariable":{"key":"site.title","styles":{"bold":true}}},{"text":{"text":"!","styles":{"italic":true}}}]}`},
		{"match spans styles", `{"content":[{"text":{"text":"Hi DS","styles":{"bold":true}}},{"text":{"text":"UB.IO!","styles":{"italic":true}}}]}`, `{"content":[{"text":{"text":"Hi ","styles":{"bold":true}}},{"siteVariable":{"key":"site.title","styles":{"bold":true}}},{"text":{"text":"!","styles":{"italic":true}}}]}`},
		{"email spans runs", `{"content":[{"text":{"text":"legal@","styles":{"bold":true}}},{"text":{"text":"dsub.io"}}]}`, `{"content":[{"siteVariable":{"key":"contact.legalEmail","styles":{"bold":true}}}]}`},
		{"descriptive mailto label", `{"content":[{"link":{"href":"mailto:legal@dsub.io","content":[{"text":"Contact our ","styles":{"bold":true}},{"text":"legal team"}]}}]}`, `{"content":[{"link":{"href":"mailto:{{contact.legalEmail}}","content":[{"text":"Contact our ","styles":{"bold":true}},{"text":"legal team"}]}}]}`},
		{"empty mailto label", `{"content":[{"link":{"href":"mailto:privacy@dsub.io","content":[]}}]}`, `{"content":[{"link":{"href":"mailto:{{contact.privacyEmail}}","content":[]}}]}`},
		{"trimmed case insensitive email label", `{"content":[{"link":{"href":"MAILTO:LEGAL@DSUB.IO","content":[{"text":" LEGAL@","styles":{"bold":true}},{"text":"DSUB.IO ","styles":{"italic":true}}]}}]}`, `{"content":[{"siteVariable":{"key":"contact.legalEmail","linked":true,"styles":{"bold":true}}}]}`},
		{"link label boundaries", `{"content":[{"link":{"href":"/about","content":[{"text":"MY"},{"text":"DSUB","styles":{"bold":true}},{"text":" https://example.test/"},{"text":"DSUB","styles":{"italic":true}}]}}]}`, `{"content":[{"link":{"href":"/about","content":[{"text":"MY"},{"text":"DSUB","styles":{"bold":true}},{"text":" https://example.test/"},{"text":"DSUB","styles":{"italic":true}}]}}]}`},
		{"link label match spans styles", `{"content":[{"link":{"href":"/about","content":[{"text":"DS","styles":{"bold":true}},{"text":"UB!","styles":{"italic":true}}]}}]}`, `{"content":[{"link":{"href":"/about","content":[{"text":"{{site.title}}","styles":{"bold":true}},{"text":"!","styles":{"italic":true}}]}}]}`},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			got, err := RichJSON([]byte(fixture.source))
			require.NoError(t, err)
			require.JSONEq(t, fixture.want, string(got))
			again, err := RichJSON(got)
			require.NoError(t, err)
			require.Equal(t, string(got), string(again))
		})
	}
}

func TestSitePathsCollapseLeadingSlashes(t *testing.T) {
	for _, source := range []string{"https://www.dsub.io//example.com/path?x=1#a", "https://dsub.io////example.com/path?x=1#a"} {
		t.Run(source, func(t *testing.T) {
			want := "/example.com/path?x=1#a"
			path, ok := sitePath(source)
			require.True(t, ok)
			require.Equal(t, want, path)
			_, ok = sitePath(path)
			require.False(t, ok)
			menu, err := MenuJSON([]byte(`[{"url":` + quotedJSON(source) + `}]`))
			require.NoError(t, err)
			require.JSONEq(t, `[{"url":`+quotedJSON(want)+`}]`, string(menu))
			again, err := MenuJSON(menu)
			require.NoError(t, err)
			require.Equal(t, menu, again)
			rich, err := RichJSON([]byte(`{"paragraph":{"content":[{"link":{"href":` + quotedJSON(source) + `,"content":[{"text":"Example"}]}}]}}`))
			require.NoError(t, err)
			require.Contains(t, string(rich), `"href":`+quotedJSON(want))
			_, err = contentv1.NormalizeContentStorageLocale("policy", "paragraph", rich, contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_RESTORE_SNAPSHOT)
			require.NoError(t, err)
			again, err = RichJSON(rich)
			require.NoError(t, err)
			require.Equal(t, rich, again)
			email := `<a href="` + source + `">Example</a>`
			require.Equal(t, `<a href="{{site.url}}`+want+`">Example</a>`, EmailSource(email))
			require.Equal(t, EmailSource(email), EmailSource(EmailSource(email)))
		})
	}
}

func quotedJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func TestDescriptiveMailtoUsesGeneratedStorageContract(t *testing.T) {
	for _, name := range []string{"legal", "privacy", "support"} {
		t.Run(name, func(t *testing.T) {
			source := []byte(`{"paragraph":{"content":[{"link":{"href":"mailto:` + name + `@dsub.io","content":[{"text":"Contact our team","styles":{"bold":true}}]}}]}}`)
			got, err := RichJSON(source)
			require.NoError(t, err)
			require.Contains(t, string(got), `"href":"mailto:{{contact.`+name+`Email}}"`)
			for _, mode := range []contentv1.ContentValidationMode{contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_RESTORE_SNAPSHOT, contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_WRITE} {
				_, err = contentv1.NormalizeContentStorageLocale("policy", "paragraph", got, mode)
				require.NoError(t, err)
			}
			again, err := RichJSON(got)
			require.NoError(t, err)
			require.Equal(t, got, again)
		})
	}
}
func TestEmailAndMenuFixtures(t *testing.T) {
	source := `<p>DSUB.IO support@dsub.io <a href="mailto:privacy@dsub.io">privacy@dsub.io</a><a href="https://www.dsub.io/my?x=1#a">My</a><a href="https://opendiscogs.dsub.io/docs">Docs</a></p>`
	want := `<p>{{site.title}} {{contact.supportEmail}} <a href="mailto:{{contact.privacyEmail}}">{{contact.privacyEmail}}</a><a href="{{site.url}}/my?x=1#a">My</a><a href="https://opendiscogs.rawsector.com/docs">Docs</a></p>`
	require.Equal(t, want, EmailSource(source))
	require.Equal(t, want, EmailSource(want))
	menu := `[{"id":"stable","url":"https://www.dsub.io/a%2Fb?x=1#a","children":[{"url":"https://dsub.io/"}]}]`
	got, err := MenuJSON([]byte(menu))
	require.NoError(t, err)
	require.JSONEq(t, `[{"id":"stable","url":"/a%2Fb?x=1#a","children":[{"url":"/"}]}]`, string(got))
	again, err := MenuJSON(got)
	require.NoError(t, err)
	require.Equal(t, string(got), string(again))
	for _, unsafe := range []string{"https://www.dsub.io.evil/path", "https://www.dsub.io@evil/path", "http://www.dsub.io/path"} {
		_, ok := sitePath(unsafe)
		require.False(t, ok)
	}
}

func TestMigrationPreservesUnrelatedAndEmptyValues(t *testing.T) {
	for _, source := range []string{`{"content":[]}`, `{"rows":[],"content":[{"text":{"text":""}}]}`, `{"content":[{"text":{"text":"{{site.title}} https://opendiscogs.dsub.io.evil/DSUB"}}]}`} {
		got, err := RichJSON([]byte(source))
		require.NoError(t, err)
		require.JSONEq(t, source, string(got))
	}
	for _, source := range []string{`<a href="https://www.dsub.io.evil/path">DSUB@example.test</a>`, `<a href="https://www.dsub.io@evil/path">Other</a>`} {
		require.Equal(t, source, EmailSource(source))
	}
	require.Equal(t, "{{contact.supportEmail}}.", Text("support@dsub.io."))
}

func TestRichMigrationUsesGeneratedPolicyStorageContract(t *testing.T) {
	source := []byte(`{"paragraph":{"content":[{"text":{"text":"DSUB legal@dsub.io"}}]}}`)
	transformed, err := RichJSON(source)
	require.NoError(t, err)
	_, err = contentv1.NormalizeContentStorageLocale("policy", "paragraph", transformed, contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_RESTORE_SNAPSHOT)
	require.NoError(t, err)
	require.Contains(t, string(transformed), `"siteVariable":{"key":"site.title"}`)
	require.Contains(t, string(transformed), `"siteVariable":{"key":"contact.legalEmail"}`)
}
