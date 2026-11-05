package translation

import (
	"strings"
	"testing"
)

// markupDocument holds one unit with a paired code around linked text, an
// escaped ampersand and a site variable placeholder.
func markupDocument() XLIFFDocument {
	return XLIFFDocument{
		Version: XLIFFVersion, SourceLocale: "en", TargetLocale: "ko",
		File: XLIFFFile{ID: "page:page-1", Groups: []XLIFFGroup{{
			ID: "block:1",
			TranslationUnit: []XLIFFUnit{{
				ID: "block:1:typed:paragraph/content", Name: "block:1:paragraph.content",
				Source:       "Read the guide & {{site.title}}",
				OriginalData: []XLIFFOriginalData{{ID: "d1"}, {ID: "d2"}, {ID: "d3", Value: "{{site.title}}"}},
				SourceInline: []XLIFFInline{
					{Kind: XLIFFInlineText, Text: "Read "},
					{Kind: XLIFFInlinePairedCode, ID: "r1", DataRefStart: "d1", DataRefEnd: "d2", CanCopy: "no", CanDelete: "no",
						Children: []XLIFFInline{{Kind: XLIFFInlineText, Text: "the guide"}}},
					{Kind: XLIFFInlineText, Text: " & "},
					{Kind: XLIFFInlinePlaceholder, ID: "p1", DataRef: "d3", CanCopy: "no", CanDelete: "no"},
				},
			}},
		}}},
	}
}

func TestXLIFFUnitMarkupRoundTripsCodesThroughImportValidation(t *testing.T) {
	units, err := XLIFFUnitsMarkup(markupDocument())
	if err != nil {
		t.Fatal(err)
	}
	want := `Read <pc id="r1">the guide</pc> &amp; <ph id="p1" equiv="{{site.title}}"/>`
	if len(units) != 1 || units[0].Source != want || units[0].GroupID != "block:1" || units[0].Target != nil {
		t.Fatalf("units = %+v, want source %q", units, want)
	}

	imported, err := XLIFFDocumentFromMarkup(markupDocument(), map[string]string{
		units[0].ID: `<pc id="r1">가이드</pc> 읽기 &amp; <ph id="p1"/>`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateXLIFFInterchangeDocument(&imported); err != nil {
		t.Fatalf("parsed target failed import validation: %v", err)
	}
	target := XLIFFTargets(imported)[units[0].ID]
	if target.TranslatedText != "가이드 읽기 & {{site.title}}" {
		t.Fatalf("target text = %q", target.TranslatedText)
	}
	if code := target.TargetInline[0]; code.DataRefStart != "d1" || code.DataRefEnd != "d2" || code.CanDelete != "no" {
		t.Fatalf("paired code did not adopt the source references: %+v", code)
	}
}

func TestXLIFFDocumentFromMarkupRejectsCodesAndUnitsTheSourceLacks(t *testing.T) {
	handle := markupDocument().File.Groups[0].TranslationUnit[0].ID
	for name, test := range map[string]struct {
		targets map[string]string
		want    string
	}{
		"unknown code":    {map[string]string{handle: `<pc id="r9">x</pc>`}, `does not match a source code`},
		"code kind swap":  {map[string]string{handle: `<ph id="r1"/>`}, `does not match a source code`},
		"unbalanced code": {map[string]string{handle: `<pc id="r1">x`}, `invalid target markup`},
		"unknown unit":    {map[string]string{"block:2:typed:x": "x"}, `not a current translation unit`},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := XLIFFDocumentFromMarkup(markupDocument(), test.targets)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}
