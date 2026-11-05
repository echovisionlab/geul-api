package translation

import (
	"strings"
	"testing"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/stretchr/testify/require"
)

func TestSiteVariableXLIFFOccurrencesAndMultiset(t *testing.T) {
	atom := func(key string) *contentv1.RichTextInline {
		return &contentv1.RichTextInline{Value: &contentv1.RichTextInline_SiteVariable{SiteVariable: &contentv1.RichTextSiteVariable{Key: key, Linked: key == "site.url"}}}
	}
	content := []*contentv1.RichTextInline{atom("site.title"), atom("site.url"), atom("site.title")}
	text, data, nodes := richTextSemanticInline(content)
	require.Len(t, nodes, 3)
	require.NotEqual(t, nodes[0].ID, nodes[2].ID)
	for _, node := range nodes {
		require.Equal(t, XLIFFInlinePlaceholder, node.Kind)
		require.Equal(t, "no", node.CanCopy)
		require.Equal(t, "no", node.CanDelete)
	}
	moved := []XLIFFInline{nodes[2], nodes[0], nodes[1]}
	translated, err := ProjectXLIFFInline(moved, data)
	require.NoError(t, err)
	result, err := applyRichTextSemanticResult(content, UnitResult{TranslatedText: translated, TargetInline: moved, OriginalData: data})
	require.NoError(t, err)
	require.Len(t, result, 3)
	require.Equal(t, "site.url", result[2].GetSiteVariable().GetKey())
	require.True(t, result[2].GetSiteVariable().GetLinked())
	for _, invalid := range [][]XLIFFInline{nodes[:2], {nodes[0], nodes[1], nodes[0]}, append(append([]XLIFFInline{}, nodes...), nodes[0])} {
		projection, err := ProjectXLIFFInline(invalid, data)
		if err == nil {
			_, err = applyRichTextSemanticResult(content, UnitResult{TranslatedText: projection, TargetInline: invalid, OriginalData: data})
		}
		require.Error(t, err)
	}
	_, err = applyRichTextSemanticResult(content, UnitResult{TranslatedText: text})
	require.Error(t, err)
}

func TestSiteTextTokensProtectedInRichTextAndLinkLabels(t *testing.T) {
	tokens := "{{site.title}} {{site.url}} {{site.companyName}} {{contact.legalEmail}} {{contact.email}} {{site.title}}"
	// Other double-brace text is protected too, as every translation placeholder always was.
	others := " {{site.unknown}} {{site.Title}} {{site.title }} {{other.value}} {{name}}"
	content := []*contentv1.RichTextInline{
		{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: tokens + others}}},
		{Value: &contentv1.RichTextInline_Link{Link: &contentv1.RichTextLink{Href: "/about", Content: []*contentv1.RichTextStyledText{{Text: tokens + others}}}}},
	}
	source, data, nodes := richTextSemanticInline(content)
	require.Len(t, data, 28) // Six paired-code data entries and twenty-two placeholder occurrences.
	var count int
	var walk func([]XLIFFInline)
	walk = func(nodes []XLIFFInline) {
		for _, node := range nodes {
			if node.Kind == XLIFFInlinePlaceholder {
				count++
				require.Equal(t, "no", node.CanCopy)
				require.Equal(t, "no", node.CanDelete)
			}
			walk(node.Children)
		}
	}
	walk(nodes)
	require.Equal(t, 22, count)
	target := source
	document := XLIFFDocument{Version: XLIFFVersion, SourceLocale: "en", TargetLocale: "ko", File: XLIFFFile{ID: "page:1", Groups: []XLIFFGroup{{ID: "body", TranslationUnit: []XLIFFUnit{{ID: "u1", Source: source, Target: &target, OriginalData: data, SourceInline: nodes, TargetInline: nodes}}}}}}
	body, err := MarshalXLIFF(&document)
	require.NoError(t, err)
	require.Equal(t, 44, strings.Count(string(body), "<ph "), "source and target each keep every placeholder")
	decoded, err := UnmarshalXLIFFInterchange(body)
	require.NoError(t, err)
	require.Equal(t, source, decoded.File.Groups[0].TranslationUnit[0].Source)

	rebuilt, err := applyRichTextSemanticResult(content, UnitResult{TranslatedText: source, TargetInline: nodes, OriginalData: data})
	require.NoError(t, err)
	require.Equal(t, tokens+others, rebuilt[1].GetLink().GetContent()[0].GetText())
	require.Equal(t, "/about", rebuilt[1].GetLink().GetHref())
	for _, target := range []string{
		strings.Replace(source, "{{site.title}}", "", 1),
		source + " {{site.title}}",
		strings.Replace(source, "{{site.title}}", "{{site.url}}", 1),
	} {
		_, err = applyRichTextSemanticResult(content, UnitResult{TranslatedText: target, TargetInline: nodes, OriginalData: data})
		require.ErrorContains(t, err, "site token multiset")
	}
}

func TestTextTemplateTokenXLIFFRoundTripAndMultiset(t *testing.T) {
	source := "{{site.title}} {{site.url}} {{site.companyName}} {{contact.legalEmail}} {{contact.email}} {{site.title}} {{unknown}}"
	document := XLIFFDocument{Version: XLIFFVersion, SourceLocale: "en", TargetLocale: "ko", File: XLIFFFile{
		ID: "page:1", Groups: []XLIFFGroup{{ID: "meta", TranslationUnit: []XLIFFUnit{{ID: "title", Source: source}}}},
	}}
	// Every double-brace placeholder, including unknown ones, stays protected.
	target := source
	document.File.Groups[0].TranslationUnit[0].Target = &target
	body, err := MarshalXLIFF(&document)
	require.NoError(t, err)
	require.Equal(t, 14, strings.Count(string(body), "<ph "))
	require.Contains(t, string(body), "{{unknown}}")
	decoded, err := UnmarshalXLIFFInterchange(body)
	require.NoError(t, err)
	require.Equal(t, target, *decoded.File.Groups[0].TranslationUnit[0].Target)
	for _, invalid := range []string{
		strings.Replace(target, "{{site.title}}", "", 1),
		target + " {{site.title}}",
		strings.Replace(target, "{{site.title}}", "{{site.url}}", 1),
	} {
		candidate := XLIFFWithTargets(document, map[string]UnitResult{"title": {TranslatedText: invalid}})
		require.Error(t, ValidateXLIFFInterchangeDocument(&candidate))
	}
}

func TestEmailTemplateStillProtectsArbitraryPlaceholders(t *testing.T) {
	for _, entity := range []string{"email_template", "email_layout"} {
		t.Run(entity, func(t *testing.T) {
			document := XLIFFDocument{Version: XLIFFVersion, SourceLocale: "en", TargetLocale: "ko", File: XLIFFFile{
				ID: entity + ":1", Groups: []XLIFFGroup{{ID: "meta", TranslationUnit: []XLIFFUnit{{ID: "title", Source: "{{name}} {{site.title}} {{custom.value}}"}}}},
			}}
			body, err := MarshalXLIFF(&document)
			require.NoError(t, err)
			require.Equal(t, 3, strings.Count(string(body), "<ph "))
			target := "{{site.title}}"
			document.File.Groups[0].TranslationUnit[0].Target = &target
			require.Error(t, ValidateXLIFFDocument(&document, true))
		})
	}
}

func TestSiteTokenPatternUsesOnlyCanonicalKeys(t *testing.T) {
	source := "{{site.title}} {{site.url}} {{site.companyName}} {{contact.legalEmail}} {{contact.email}} {{contact.supportEmail}} {{contact.privacyEmail}}"
	require.Equal(t, []string{"{{site.title}}", "{{site.url}}", "{{site.companyName}}", "{{contact.legalEmail}}", "{{contact.email}}"}, siteTokenPattern.FindAllString(source, -1))
}
