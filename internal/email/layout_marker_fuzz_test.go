package email

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var updateGolden = flag.Bool("update", false, "rewrite golden files under testdata/")

const (
	layoutFuzzTextID    = "11111111-1111-4111-8111-111111111111"
	layoutFuzzElementID = "22222222-2222-4222-8222-222222222222"
	layoutFuzzSecondID  = "33333333-3333-4333-8333-333333333333"
)

// layoutMarkerSeeds pin marker extraction for canonical sources, sparse
// overlays and every fail-closed marker shape. They are also the fuzz seeds.
var layoutMarkerSeeds = []struct {
	name    string
	content string
}{
	{"empty", ``},
	{"unmarked_plain_text", `<p>Hello</p>`},
	{"canonical_text_unit", `<p><!--geul-unit:text:` + layoutFuzzTextID + `-->Hello</p>`},
	{"canonical_explicit_empty_text", `<p><!--geul-unit:text:` + layoutFuzzTextID + `--></p>`},
	{"canonical_attribute_unit", `<!--geul-unit:element:` + layoutFuzzElementID + `--><img alt="Logo" title="Brand" src="/logo.png">`},
	{"canonical_mixed_units", `<main>{{content}}</main><footer><!--geul-unit:text:` + layoutFuzzTextID + `-->Footer &amp; more<!--geul-unit:element:` + layoutFuzzElementID + `--><a aria-label="Privacy" href="/privacy"><!--geul-unit:text:` + layoutFuzzSecondID + `-->Privacy</a></footer>`},
	{"canonical_excluded_elements", `<head><title>Title</title><style>.a{}</style></head><body><script>var a = "x";</script><p><!--geul-unit:text:` + layoutFuzzTextID + `-->Body</p></body>`},
	{"canonical_placeholder_only", `<p>{{content}}</p><img alt="{{alt}}">`},
	{"canonical_line_breaks", "<p><!--geul-unit:text:" + layoutFuzzTextID + "-->First\r\nSecond\rThird</p>"},
	{"overlay_text_value", `<!--geul-overlay:v1--><p><!--geul-unit:text:` + layoutFuzzTextID + `--><!--geul-value:text:` + layoutFuzzTextID + `-->Bonjour</p>`},
	{"overlay_explicit_empty_text", `<!--geul-overlay:v1--><p><!--geul-unit:text:` + layoutFuzzTextID + `--><!--geul-value:text:` + layoutFuzzTextID + `--></p>`},
	{"overlay_attribute_value", `<!--geul-overlay:v1--><!--geul-unit:element:` + layoutFuzzElementID + `--><!--geul-value:element:` + layoutFuzzElementID + `:alt--><img alt="Logo FR" title="Brand">`},
	{"overlay_explicit_empty_text_before_element", `<!--geul-overlay:v1--><p><!--geul-unit:text:` + layoutFuzzTextID + `--><!--geul-value:text:` + layoutFuzzTextID + `--><b><!--geul-unit:text:` + layoutFuzzSecondID + `-->world</b></p>`},
	{"overlay_omitted_value", `<!--geul-overlay:v1--><p><!--geul-unit:text:` + layoutFuzzTextID + `-->Source fallback</p>`},
	{"overlay_missing_marker", `<p><!--geul-unit:text:` + layoutFuzzTextID + `--><!--geul-value:text:` + layoutFuzzTextID + `-->Bonjour</p>`},
	{"overlay_duplicated", `<!--geul-overlay:v1--><!--geul-overlay:v1--><p>x</p>`},
	{"overlay_detached_value", `<!--geul-overlay:v1--><!--geul-value:text:` + layoutFuzzTextID + `-->Bonjour`},
	{"overlay_value_kind_mismatch", `<!--geul-overlay:v1--><!--geul-unit:text:` + layoutFuzzTextID + `--><!--geul-value:element:` + layoutFuzzTextID + `:alt-->x`},
	{"overlay_missing_attribute", `<!--geul-overlay:v1--><!--geul-unit:element:` + layoutFuzzElementID + `--><!--geul-value:element:` + layoutFuzzElementID + `:title--><img alt="x">`},
	{"overlay_unsupported_attribute", `<!--geul-overlay:v1--><!--geul-unit:element:` + layoutFuzzElementID + `--><!--geul-value:element:` + layoutFuzzElementID + `:href--><a href="/">x</a>`},
	{"raw_text_element_text", `<textarea>Note</textarea><xmp>Code</xmp><!--geul-unit:element:` + layoutFuzzElementID + `--><iframe title="Frame">x</iframe>`},
	{"missing_text_marker", `<footer>Unmarked footer</footer>`},
	{"missing_element_marker", `<img alt="Unmarked">`},
	{"detached_element_marker", `<!--geul-unit:element:` + layoutFuzzElementID + `-->text`},
	{"element_marker_without_attributes", `<!--geul-unit:element:` + layoutFuzzElementID + `--><div>x</div>`},
	{"duplicate_unit_marker", `<!--geul-unit:text:` + layoutFuzzTextID + `-->One<!--geul-unit:text:` + layoutFuzzTextID + `-->Two`},
	{"malformed_unit_marker", `<!--geul-unit:text-->x`},
	{"unsupported_unit_kind", `<!--geul-unit:image:` + layoutFuzzTextID + `-->x`},
	{"non_canonical_unit_uuid", `<!--geul-unit:text:AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA-->x`},
	{"nil_unit_uuid", `<!--geul-unit:text:00000000-0000-0000-0000-000000000000-->x`},
	{"source_with_value_marker", `<!--geul-value:text:` + layoutFuzzTextID + `-->x`},
}

type layoutMarkerGolden struct {
	Units       []LayoutContentUnit `json:"units,omitempty"`
	UnitsError  string              `json:"units_error,omitempty"`
	Values      map[string]string   `json:"locale_values,omitempty"`
	ValuesError string              `json:"locale_values_error,omitempty"`
}

func TestLayoutMarkerExtractionGolden(t *testing.T) {
	got := make(map[string]layoutMarkerGolden, len(layoutMarkerSeeds))
	for _, seed := range layoutMarkerSeeds {
		var entry layoutMarkerGolden
		units, err := ExtractLayoutContentUnits(seed.content)
		if err != nil {
			entry.UnitsError = err.Error()
		} else {
			entry.Units = units
		}
		values, err := ExtractLayoutLocaleValues(seed.content)
		if err != nil {
			entry.ValuesError = err.Error()
		} else {
			entry.Values = values
		}
		got[seed.name] = entry
	}
	assertGoldenJSON(t, filepath.Join("testdata", "layout_marker_extraction.golden.json"), got)
}

// FuzzLayoutMarkerRoundTrip checks the invariants the marker codec relies on:
// extraction never panics, canonicalization is idempotent and always yields an
// extractable source, and every explicit locale value written by
// ApplyLayoutLocaleValues is read back by ExtractLayoutLocaleValues.
func FuzzLayoutMarkerRoundTrip(f *testing.F) {
	for _, seed := range layoutMarkerSeeds {
		f.Add(seed.content, "Traduction")
	}
	f.Add(`<p>Hello <b>world</b></p><img alt="Logo" title=" ">`, "Bonjour")
	// Explicit empty text values before an inline element were unreadable.
	f.Add(`<p>Hello <b>world</b></p>`, "")
	// Raw-text and RCDATA elements read marker comments as literal text.
	f.Add(`<textarea>Note</textarea>`, "Traduction")
	f.Add("<p>  spaced  </p>", "\n")
	f.Add(`<p>Source</p>`, "  a <b> & \"q\"  ")
	f.Fuzz(func(t *testing.T, content, value string) {
		_, _ = ExtractLayoutContentUnits(content)
		_, _ = ExtractLayoutLocaleValues(content)
		_, _ = ExtractLayoutStoredLocaleValues(content)
		if _, err := StripLayoutPrivateMarkers(content); err != nil {
			return
		}

		canonical, err := CanonicalizeLayoutSourceMarkers(content)
		if err != nil {
			return
		}
		units, err := ExtractLayoutContentUnits(canonical)
		if err != nil {
			t.Fatalf("canonical source is not extractable: %v\ninput: %q\ncanonical: %q", err, content, canonical)
		}
		again, err := CanonicalizeLayoutSourceMarkers(canonical)
		if err != nil || again != canonical {
			t.Fatalf("canonicalization is not idempotent (err=%v)\nfirst:  %q\nsecond: %q", err, canonical, again)
		}

		values := make(map[string]string, len(units))
		for _, unit := range units {
			values[unit.Handle] = value
		}
		encoded, _, err := ApplyLayoutLocaleValues(canonical, values)
		if err != nil {
			t.Fatalf("apply locale values to canonical source: %v\ncanonical: %q", err, canonical)
		}
		extracted, err := ExtractLayoutLocaleValues(*encoded)
		if err != nil {
			t.Fatalf("extract applied locale values: %v\nencoded: %q", err, *encoded)
		}
		if len(extracted) != len(units) {
			t.Fatalf("extracted %d values for %d units\nencoded: %q", len(extracted), len(units), *encoded)
		}
		for _, unit := range units {
			// The HTML tokenizer normalizes CR and CRLF to LF and NUL in attribute
			// values to U+FFFD when it reads the stored value back; the
			// comparison applies the same normalization.
			want := normalizeLayoutFuzzValue(layoutTranslatedValue(unit.SourceValue, value))
			if got := normalizeLayoutFuzzValue(extracted[unit.Handle]); got != want {
				t.Fatalf("value for %s = %q, want %q\nencoded: %q", unit.Handle, got, want, *encoded)
			}
		}
		if _, err := StripLayoutPrivateMarkers(*encoded); err != nil {
			t.Fatalf("strip private markers from applied overlay: %v", err)
		}
	})
}

func normalizeLayoutFuzzValue(value string) string {
	value = strings.ReplaceAll(value, "\x00", "\uFFFD")
	return strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
}

func assertGoldenJSON(t *testing.T, path string, value any) {
	t.Helper()
	encoded, err := json.MarshalIndent(value, "", "  ")
	require.NoError(t, err)
	encoded = append(encoded, '\n')
	if *updateGolden {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, encoded, 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "missing golden file; run go test -run %s -update", t.Name())
	if string(want) != string(encoded) {
		t.Fatalf("%s is stale; review the change and rerun with -update\n%s", path, firstGoldenDifference(string(want), string(encoded)))
	}
}

func firstGoldenDifference(want, got string) string {
	wantLines, gotLines := strings.Split(want, "\n"), strings.Split(got, "\n")
	for index := 0; index < len(wantLines) || index < len(gotLines); index++ {
		var wantLine, gotLine string
		if index < len(wantLines) {
			wantLine = wantLines[index]
		}
		if index < len(gotLines) {
			gotLine = gotLines[index]
		}
		if wantLine != gotLine {
			return fmt.Sprintf("line %d:\n- %s\n+ %s", index+1, wantLine, gotLine)
		}
	}
	return ""
}
