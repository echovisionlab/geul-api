package richtextinline

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var updateGolden = flag.Bool("update", false, "rewrite golden files under testdata/")

// storageLocaleSeeds pin the stored locale JSON rewrite for every envelope
// shape the normalizer walks. They are also the fuzz seeds.
var storageLocaleSeeds = []struct {
	name  string
	input string
}{
	{"no_escape_shortcut", `{"paragraph":{"content":[{"text":{"text":"plain"}}]}}`},
	{"paragraph_lf", `{"paragraph":{"content":[{"text":{"text":"a\nb"}}]}}`},
	{"paragraph_unicode_escape_lf", `{"paragraph":{"content":[{"text":{"text":"a\u000ab"}}]}}`},
	{"paragraph_crlf_edges", `{"paragraph":{"content":[{"text":{"text":"\r\na\r\rb\n"}}]}}`},
	{"styled_text", `{"paragraph":{"content":[{"text":{"text":"a\nb","styles":{"bold":true,"italic":true}}}]}}`},
	{"link_runs", `{"content":[{"link":{"href":"/","content":[{"text":"a\n"},{"text":"b","styles":{"code":true}},{"text":"\nc"}]}}]}`},
	{"link_only_breaks", `{"content":[{"link":{"href":"/","content":[{"text":"\n"}]}}]}`},
	{"atoms_preserved", `{"content":[{"siteVariable":{"key":"site.title"}},{"hardBreak":{}},{"text":{"text":"x\ny"}}]}`},
	{"table_cells", `{"table":{"content":{"rows":[{"rowId":"r","cells":[{"cellId":"c1","content":[{"text":{"text":"a\nb"}}]},{"cellId":"c2","content":[{"text":{"text":"same"}}]}]}]}}}`},
	{"table_without_rows", `{"content":{"caption":"a\nb"}}`},
	{"escaped_backslash_only", `{"content":[{"text":{"text":"a\\nb"}}]}`},
	{"plain_string_content", `{"content":"a\nb"}`},
	{"props_untouched", `{"props":{"source":"a\nb"},"paragraph":{"content":[{"text":{"text":"x\ny"}}]}}`},
	{"invalid_json", `{"content":[\n`},
	{"invalid_inline", `{"content":[{"unknown":"a\nb"}]}`},
	{"invalid_cells", `{"content":{"rows":[{"cells":"a\nb"}]}}`},
}

type storageLocaleGolden struct {
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

func TestCanonicalizeStorageLocaleGolden(t *testing.T) {
	got := make(map[string]storageLocaleGolden, len(storageLocaleSeeds))
	for _, seed := range storageLocaleSeeds {
		output, err := CanonicalizeStorageLocale(json.RawMessage(seed.input))
		if err != nil {
			// protobuf-go randomizes the separator after "proto:" per build.
			got[seed.name] = storageLocaleGolden{Error: strings.ReplaceAll(err.Error(), "\u00a0", " ")}
			continue
		}
		got[seed.name] = storageLocaleGolden{Output: string(output)}
	}
	assertGoldenJSON(t, filepath.Join("testdata", "storage_locale.golden.json"), got)
}

// FuzzCanonicalizeStorageLocale checks that a successful rewrite is valid
// JSON whenever its input was, and that rewriting its own output is a no-op.
func FuzzCanonicalizeStorageLocale(f *testing.F) {
	for _, seed := range storageLocaleSeeds {
		f.Add([]byte(seed.input))
	}
	f.Add([]byte(`{"content":{"rows":[{"cells":[{"content":[{"text":{"text":"a\nb"}}]},{"content":[{"&x":{}}]}]}]}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		input := append([]byte(nil), data...)
		output, err := CanonicalizeStorageLocale(input)
		if !bytes.Equal(input, data) {
			t.Fatalf("input was mutated: %q -> %q", data, input)
		}
		if err != nil {
			return
		}
		if json.Valid(data) && !json.Valid(output) {
			t.Fatalf("rewrite produced invalid JSON\ninput:  %q\noutput: %q", data, output)
		}
		again, err := CanonicalizeStorageLocale(output)
		if err != nil {
			// Content the first pass never had to decode (no escape, so no
			// possible line break) is re-decoded once json.Marshal escapes HTML
			// characters. Invalid inline entries are outside the normalizer's
			// contract; generated block adapters reject them on write.
			if !inlineEntriesDecodeForTest(data) {
				return
			}
			t.Fatalf("rewrite of canonical output failed: %v\noutput: %q", err, output)
		}
		if !bytes.Equal(again, output) {
			t.Fatalf("rewrite is not idempotent\nfirst:  %q\nsecond: %q", output, again)
		}
	})
}

// FuzzCanonicalize checks the inline invariants: no text or link run keeps a
// line break, the visible text with breaks as LF equals the normalized input,
// the result is a fixed point and the input messages are not mutated.
func FuzzCanonicalize(f *testing.F) {
	f.Add("plain", "link", true)
	f.Add("\na\r\n\nb\r", "c\nd", true)
	f.Add("", "\n", false)
	f.Add("\r", "", false)
	f.Add("a\r\r\nb", "\r\n\r\n", true)
	f.Add("0\r", "\n", true)
	f.Fuzz(func(t *testing.T, text, linkText string, bold bool) {
		input := []*contentv1.RichTextInline{
			{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: text, Styles: &contentv1.RichTextStyle{Bold: &bold}}}},
			{Value: &contentv1.RichTextInline_Link{Link: &contentv1.RichTextLink{Href: "https://example.test", Content: []*contentv1.RichTextStyledText{{Text: linkText}, {Text: text}}}}},
			{Value: &contentv1.RichTextInline_SiteVariable{SiteVariable: &contentv1.RichTextSiteVariable{Key: "site.title"}}},
			{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: linkText}}},
		}
		before := make([]*contentv1.RichTextInline, len(input))
		for index, item := range input {
			before[index] = proto.Clone(item).(*contentv1.RichTextInline)
		}
		got := Canonicalize(input)
		for index := range input {
			if !proto.Equal(before[index], input[index]) {
				t.Fatalf("input item %d was mutated", index)
			}
		}
		for _, item := range got {
			if strings.ContainsAny(item.GetText().GetText(), "\r\n") || linkHasNewlines(item.GetLink()) {
				t.Fatalf("line break survived canonicalization: %v", item)
			}
			if run := item.GetText(); run != nil && run.GetStyles().GetBold() != (run.GetStyles() != nil && bold) {
				t.Fatalf("text run lost its style: %v", item)
			}
		}
		if want, have := flattenInlineForTest(input), flattenInlineForTest(got); have != want {
			t.Fatalf("visible text changed\nwant: %q\ngot:  %q", want, have)
		}
		again := Canonicalize(got)
		if len(again) != len(got) {
			t.Fatalf("canonical output is not a fixed point: %d -> %d items", len(got), len(again))
		}
		for index := range got {
			if !proto.Equal(got[index], again[index]) {
				t.Fatalf("canonical output is not a fixed point at item %d", index)
			}
		}
	})
}

// flattenInlineForTest renders visible text with breaks as LF. Each run is
// normalized on its own: a CR ending one run and an LF starting the next are
// two breaks, not one CRLF.
func flattenInlineForTest(items []*contentv1.RichTextInline) string {
	var text strings.Builder
	for _, item := range items {
		switch {
		case item.GetText() != nil:
			text.WriteString(NormalizeNewlines(item.GetText().GetText()))
		case item.GetLink() != nil:
			for _, run := range item.GetLink().GetContent() {
				text.WriteString(NormalizeNewlines(run.GetText()))
			}
		case item.GetHardBreak() != nil:
			text.WriteString("\n")
		case item.GetSiteVariable() != nil:
			text.WriteString("{" + item.GetSiteVariable().GetKey() + "}")
		}
	}
	return text.String()
}

func inlineEntriesDecodeForTest(data []byte) bool {
	var root any
	if json.Unmarshal(data, &root) != nil {
		return false
	}
	var valid func(value any) bool
	valid = func(value any) bool {
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				if entries, ok := child.([]any); ok && key == "content" {
					for _, entry := range entries {
						encoded, err := json.Marshal(entry)
						if err != nil || protojson.Unmarshal(encoded, &contentv1.RichTextInline{}) != nil {
							return false
						}
					}
				}
				if !valid(child) {
					return false
				}
			}
		case []any:
			for _, child := range typed {
				if !valid(child) {
					return false
				}
			}
		}
		return true
	}
	return valid(root)
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
