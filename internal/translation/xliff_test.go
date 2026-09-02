package translation

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildAndMarshalXLIFFDocument(t *testing.T) {
	contextTitle := "Account security email"
	context := "unsubscribe link"
	plan := &ExtractionPlan{
		EntityType: "email_layout", EntityID: "layout-1", SourceLocale: "en", TargetLocale: "it",
		ContextTitle: &contextTitle,
		Bundles: []Bundle{{
			BundleID: "html:main", SequenceTotal: 1,
			Units: []Unit{{
				UnitID: "html:text:0", Path: "entity:content_html:text:0",
				FieldName: "content_html", SourceFormat: SourceFormatHTMLText,
				ContainerType: ContainerTypeHTMLNode, ContainerID: "0",
				SourceText: " Unsubscribe from {{site_name}} ", Context: &context,
			}},
		}},
	}

	document, err := BuildXLIFFDocument(plan)
	require.NoError(t, err)
	target := " Annulla l'iscrizione a {{site_name}} "
	document.File.Groups[0].TranslationUnit[0].Target = &target
	require.NoError(t, ValidateXLIFFDocument(document, true))

	body, err := MarshalXLIFF(document)
	require.NoError(t, err)
	xliff := string(body)
	require.Contains(t, xliff, `xmlns="urn:oasis:names:tc:xliff:document:2.2"`)
	require.Contains(t, xliff, `version="2.2"`)
	require.Contains(t, xliff, `srcLang="en"`)
	require.Contains(t, xliff, `trgLang="it"`)
	require.Contains(t, xliff, `<group id="html:main">`)
	require.Contains(t, xliff, `<unit id="html:text:0" canResegment="no"`)
	require.Contains(t, xliff, `<data id="d1">{{site_name}}</data>`)
	require.Equal(t, 2, strings.Count(xliff, `id="p1"`))
	require.NotContains(t, xliff, "Unsubscribe from {{site_name}}")
}

func TestUnmarshalXLIFFInterchangePreservesExplicitEmptyTarget(t *testing.T) {
	body := []byte(`<?xml version="1.0" encoding="UTF-8"?><xliff xmlns="urn:oasis:names:tc:xliff:document:2.2" version="2.2" srcLang="ko" trgLang="en"><file id="post:00000000-0000-0000-0000-000000000001"><group id="meta"><unit id="entity:title" name="title"><segment id="s1"><source>제목</source><target></target></segment></unit></group></file></xliff>`)
	document, err := UnmarshalXLIFFInterchange(body)
	if err != nil {
		t.Fatalf("UnmarshalXLIFFInterchange() error = %v", err)
	}
	target := document.File.Groups[0].TranslationUnit[0].Target
	if target == nil || *target != "" {
		t.Fatalf("target = %#v, want explicit empty", target)
	}
	if _, err := UnmarshalXLIFF(body); err == nil {
		t.Fatal("provider parser accepted an empty translated target")
	}
}

func TestXLIFFRoundTripPreservesExplicitEmptyAndWhitespaceOnlyUnits(t *testing.T) {
	plan := &ExtractionPlan{
		EntityType: "post", EntityID: "post-1", SourceLocale: "ko", TargetLocale: "en",
		Bundles: []Bundle{{
			BundleID: "body", Units: []Unit{
				{UnitID: "body:empty", SourceText: ""},
				{UnitID: "body:whitespace", SourceText: " \t "},
			},
		}},
	}
	document, err := BuildXLIFFDocument(plan)
	require.NoError(t, err)
	emptyTarget := ""
	whitespaceTarget := " \t "
	document.File.Groups[0].TranslationUnit[0].Target = &emptyTarget
	document.File.Groups[0].TranslationUnit[1].Target = &whitespaceTarget
	require.NoError(t, ValidateXLIFFDocument(document, true))

	body, err := MarshalXLIFF(document)
	require.NoError(t, err)
	require.Contains(t, string(body), `<unit id="body:empty"`)
	require.Contains(t, string(body), `<source xml:space="preserve"></source>`)

	decoded, err := UnmarshalXLIFF(body)
	require.NoError(t, err)
	units := decoded.File.Groups[0].TranslationUnit
	require.Len(t, units, 2)
	require.Equal(t, "body:empty", units[0].ID)
	require.Equal(t, "", units[0].Source)
	require.NotNil(t, units[0].Target)
	require.Equal(t, "", *units[0].Target)
	require.Equal(t, "body:whitespace", units[1].ID)
	require.Equal(t, " \t ", units[1].Source)
	require.Equal(t, []XLIFFInline{{Kind: XLIFFInlineText, Text: " \t "}}, units[1].SourceInline)
	require.NotNil(t, units[1].Target)
	require.Equal(t, " \t ", *units[1].Target)
	require.Equal(t, []XLIFFInline{{Kind: XLIFFInlineText, Text: " \t "}}, units[1].TargetInline)
}

func TestUnmarshalXLIFFInterchangeRejectsExtensionsAndRemoteReferences(t *testing.T) {
	base := `<?xml version="1.0"?><xliff xmlns="urn:oasis:names:tc:xliff:document:2.2" version="2.2" srcLang="ko" trgLang="en"><file id="post:1"><group id="meta"><unit id="title"><segment id="s1"><source>제목</source><target>Title</target></segment></unit>%s</group></file></xliff>`
	for name, extension := range map[string]string{
		"extension element": `<resourceData xmlns="urn:oasis:names:tc:xliff:resourcedata:2.0"/>`,
		"remote href":       `<notes href="https://example.invalid/source"><note category="context">x</note></notes>`,
		"DTD":               `<!DOCTYPE xliff SYSTEM "https://example.invalid/xliff.dtd">`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := UnmarshalXLIFFInterchange([]byte(fmt.Sprintf(base, extension))); err == nil {
				t.Fatal("unsupported XLIFF profile input was accepted")
			}
		})
	}
}

func TestUnmarshalXLIFFRejectsLegacy21Namespace(t *testing.T) {
	body := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<xliff xmlns="urn:oasis:names:tc:xliff:document:2.0" version="2.1" srcLang="en" trgLang="ko">
  <file id="post:post-1"><group id="block:a"><unit id="block:a:text"><segment id="s1"><source>Hello</source></segment></unit></group></file>
</xliff>`)

	_, err := UnmarshalXLIFF(body)
	require.ErrorContains(t, err, "unsupported XLIFF namespace")
}

func TestValidateXLIFFDocumentRejectsPlaceholderAndLineBreakDrift(t *testing.T) {
	document := &XLIFFDocument{
		Version: XLIFFVersion, SourceLocale: "en", TargetLocale: "ko",
		File: XLIFFFile{ID: "email_template:template-1", Groups: []XLIFFGroup{{
			ID: "entity:meta", TranslationUnit: []XLIFFUnit{{
				ID: "entity:subject", Source: "Hello {{name}}\nWelcome",
			}},
		}}},
	}

	missingPlaceholder := "안녕하세요\n환영합니다"
	document.File.Groups[0].TranslationUnit[0].Target = &missingPlaceholder
	require.ErrorContains(t, ValidateXLIFFDocument(document, true), "placeholder set changed")

	flattened := "안녕하세요 {{name}} 환영합니다"
	document.File.Groups[0].TranslationUnit[0].Target = &flattened
	require.ErrorContains(t, ValidateXLIFFDocument(document, true), "line-break structure changed")
}

func TestValidateXLIFFDocumentRejectsDuplicateUnitIdentity(t *testing.T) {
	document := &XLIFFDocument{
		Version: XLIFFVersion, SourceLocale: "en", TargetLocale: "fr",
		File: XLIFFFile{ID: "post:post-1", Groups: []XLIFFGroup{
			{ID: "block:a", TranslationUnit: []XLIFFUnit{{ID: "block:text", Source: "One"}}},
			{ID: "block:b", TranslationUnit: []XLIFFUnit{{ID: "block:text", Source: "Two"}}},
		}},
	}

	require.ErrorContains(t, ValidateXLIFFDocument(document, false), `duplicate XLIFF unit id "block:text"`)
}

func TestMarshalXLIFFUsesDistinctIDsForRepeatedPlaceholderOccurrences(t *testing.T) {
	document := &XLIFFDocument{
		Version: XLIFFVersion, SourceLocale: "en", TargetLocale: "ko",
		File: XLIFFFile{ID: "email_template:template-1", Groups: []XLIFFGroup{{
			ID: "entity:meta", TranslationUnit: []XLIFFUnit{{
				ID: "entity:body", Source: "Hello {{name}}, {{name}}", Target: xliffTestStringPtr("안녕하세요 {{name}}, {{name}}"),
			}},
		}}},
	}

	body, err := MarshalXLIFF(document)
	require.NoError(t, err)
	xliff := string(body)
	require.Equal(t, 2, strings.Count(xliff, `id="p1"`))
	require.Equal(t, 2, strings.Count(xliff, `id="p2"`))
	require.Equal(t, 2, strings.Count(xliff, `dataRef="d1"`))
	require.Equal(t, 2, strings.Count(xliff, `dataRef="d2"`))
}

func TestSamePlaceholdersChecksOccurrenceCardinality(t *testing.T) {
	require.True(t, SamePlaceholders("{{name}} {{name}} {{site}}", "{{site}} {{name}} {{name}}"))
	require.False(t, SamePlaceholders("{{name}} {{name}} {{site}}", "{{name}} {{site}} {{site}}"))
}

func TestValidateResponseRejectsXLIFFIdentityAndSourceChanges(t *testing.T) {
	expected := XLIFFDocument{
		Version: XLIFFVersion, SourceLocale: "en", TargetLocale: "ko",
		File: XLIFFFile{ID: "page:page-1", Groups: []XLIFFGroup{{
			ID: "entity:meta", TranslationUnit: []XLIFFUnit{{ID: "entity:title", Source: "Hello"}},
		}}},
	}
	actual := XLIFFWithTargets(expected, map[string]UnitResult{
		"entity:title": {UnitID: "entity:title", TranslatedText: "안녕하세요"},
	})
	actual.Version = "2.0"
	actual.SourceLocale = "fr"
	actual.TargetLocale = "ja"
	actual.File.ID = "page:other"
	actual.File.Groups[0].ID = "other:group"
	actual.File.Groups[0].TranslationUnit[0].Source = "Changed"

	result := ValidateResponse(expected, ProviderResponse{Document: actual})
	require.False(t, result.Passed)
	require.NotNil(t, result.ParseError)
	require.Contains(t, *result.ParseError, "XLIFF version changed")
	require.Contains(t, *result.ParseError, "XLIFF source locale changed")
	require.Contains(t, *result.ParseError, "XLIFF target locale changed")
	require.Contains(t, *result.ParseError, "XLIFF file id changed")
	require.Contains(t, *result.ParseError, `XLIFF group "entity:meta" changed`)
	require.Contains(t, *result.ParseError, `XLIFF unit "entity:title" source changed`)
}

func TestXLIFFTypedInlineRoundTripsNestedPairedCodesAndPlaceholders(t *testing.T) {
	source := `<strong>Hello {{name}}</strong>`
	target := `<strong>안녕하세요 {{name}}</strong>`
	document := &XLIFFDocument{
		Version: XLIFFVersion, SourceLocale: "en", TargetLocale: "ko",
		File: XLIFFFile{ID: "email_template:template-1", Groups: []XLIFFGroup{{
			ID: "entity:body", TranslationUnit: []XLIFFUnit{{
				ID: "entity:body", Source: source, Target: &target,
				OriginalData: []XLIFFOriginalData{
					{ID: "d1", Value: "<strong>"},
					{ID: "d2", Value: "</strong>"},
					{ID: "d3", Value: "{{name}}"},
				},
				SourceInline: []XLIFFInline{{
					Kind: XLIFFInlinePairedCode, ID: "c1", DataRefStart: "d1", DataRefEnd: "d2",
					CanCopy: "no", CanDelete: "no", Children: []XLIFFInline{
						{Kind: XLIFFInlineText, Text: "Hello "},
						{Kind: XLIFFInlinePlaceholder, ID: "p1", DataRef: "d3", CanCopy: "no", CanDelete: "no"},
					},
				}},
				TargetInline: []XLIFFInline{{
					Kind: XLIFFInlinePairedCode, ID: "c1", DataRefStart: "d1", DataRefEnd: "d2",
					CanCopy: "no", CanDelete: "no", Children: []XLIFFInline{
						{Kind: XLIFFInlineText, Text: "안녕하세요 "},
						{Kind: XLIFFInlinePlaceholder, ID: "p1", DataRef: "d3", CanCopy: "no", CanDelete: "no"},
					},
				}},
			}},
		}}},
	}

	body, err := MarshalXLIFF(document)
	require.NoError(t, err)
	require.Contains(t, string(body), `<pc id="c1" dataRefStart="d1" dataRefEnd="d2" canCopy="no" canDelete="no">`)
	require.Contains(t, string(body), `<ph id="p1" dataRef="d3" canCopy="no" canDelete="no"></ph>`)

	decoded, err := UnmarshalXLIFF(body)
	require.NoError(t, err)
	decodedUnit := decoded.File.Groups[0].TranslationUnit[0]
	require.Equal(t, source, decodedUnit.Source)
	require.Equal(t, target, *decodedUnit.Target)
	require.True(t, reflect.DeepEqual(document.File.Groups[0].TranslationUnit[0].SourceInline, decodedUnit.SourceInline))
	require.True(t, reflect.DeepEqual(document.File.Groups[0].TranslationUnit[0].TargetInline, decodedUnit.TargetInline))
}

func TestParseTranslatedXLIFFRejectsInlineSourceIdentityDrift(t *testing.T) {
	target := "Hello"
	document := XLIFFDocument{
		Version: XLIFFVersion, SourceLocale: "en", TargetLocale: "fr",
		File: XLIFFFile{ID: "page:page-1", Groups: []XLIFFGroup{{
			ID: "entity:body", TranslationUnit: []XLIFFUnit{{ID: "entity:body", Source: "Hello", Target: &target}},
		}}},
	}
	body, err := MarshalXLIFF(&document)
	require.NoError(t, err)
	drifted := strings.Replace(string(body), `unit id="entity:body"`, `unit id="entity:changed"`, 1)

	_, err = ParseTranslatedXLIFF(document, []byte(drifted))
	require.ErrorContains(t, err, "unit identity changed")
}

func TestValidateXLIFFRejectsTargetTextOutsideSourceTextBearingRun(t *testing.T) {
	sourceInline := []XLIFFInline{{
		Kind: XLIFFInlinePairedCode, ID: "l1", DataRefStart: "d1", DataRefEnd: "d2",
		Children: []XLIFFInline{{
			Kind: XLIFFInlinePairedCode, ID: "r1", DataRefStart: "d3", DataRefEnd: "d4",
			Children: []XLIFFInline{{Kind: XLIFFInlineText, Text: "Hello"}},
		}},
	}}
	data := []XLIFFOriginalData{{ID: "d1"}, {ID: "d2"}, {ID: "d3"}, {ID: "d4"}}
	for _, test := range []struct {
		name   string
		target []XLIFFInline
	}{
		{name: "top level", target: []XLIFFInline{
			{Kind: XLIFFInlineText, Text: "Bonjour"},
			{Kind: XLIFFInlinePairedCode, ID: "l1", DataRefStart: "d1", DataRefEnd: "d2", Children: []XLIFFInline{{Kind: XLIFFInlinePairedCode, ID: "r1", DataRefStart: "d3", DataRefEnd: "d4"}}},
		}},
		{name: "directly under link", target: []XLIFFInline{{
			Kind: XLIFFInlinePairedCode, ID: "l1", DataRefStart: "d1", DataRefEnd: "d2",
			Children: []XLIFFInline{{Kind: XLIFFInlineText, Text: "Bonjour"}, {Kind: XLIFFInlinePairedCode, ID: "r1", DataRefStart: "d3", DataRefEnd: "d4"}},
		}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := "Bonjour"
			document := XLIFFDocument{Version: XLIFFVersion, SourceLocale: "en", TargetLocale: "fr", File: XLIFFFile{
				ID: "post:1", Groups: []XLIFFGroup{{ID: "body", TranslationUnit: []XLIFFUnit{{
					ID: "u1", Source: "Hello", Target: &target, OriginalData: data,
					SourceInline: sourceInline, TargetInline: test.target,
				}}}},
			}}
			require.ErrorContains(t, ValidateXLIFFDocument(&document, true), "target text moved outside")
		})
	}
}

func xliffTestStringPtr(value string) *string { return &value }
