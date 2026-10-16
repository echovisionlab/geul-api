package richtextinline

import (
	"encoding/json"
	"testing"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestCanonicalizeLineBreaksPreservesStylesLinksAtomsAndInput(t *testing.T) {
	bold := true
	input := []*contentv1.RichTextInline{
		{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: "\na\r\n\nb\r", Styles: &contentv1.RichTextStyle{Bold: &bold}}}},
		{Value: &contentv1.RichTextInline_Link{Link: &contentv1.RichTextLink{Href: "https://example.test", Content: []*contentv1.RichTextStyledText{{Text: "c\nd"}}}}},
		{Value: &contentv1.RichTextInline_SiteVariable{SiteVariable: &contentv1.RichTextSiteVariable{Key: "site.title"}}},
	}
	before := proto.Clone(input[0])
	got := Canonicalize(input)
	require.Len(t, got, 10)
	require.True(t, proto.Equal(before, input[0]))
	for _, index := range []int{0, 2, 3, 5, 7} {
		require.NotNil(t, got[index].GetHardBreak())
	}
	require.True(t, got[1].GetText().GetStyles().GetBold())
	require.True(t, got[4].GetText().GetStyles().GetBold())
	require.Equal(t, "https://example.test", got[6].GetLink().GetHref())
	require.Equal(t, "https://example.test", got[8].GetLink().GetHref())
	require.Equal(t, "site.title", got[9].GetSiteVariable().GetKey())
	require.Equal(t, got, Canonicalize(got))
}

func TestCanonicalizeStorageLocaleIsIdempotentAndLeavesSourcesAlone(t *testing.T) {
	for _, input := range []string{
		`{"paragraph":{"content":[{"text":{"text":"a\u000ab"}}]}}`,
		`{"paragraph":{"content":[{"text":{"text":"a\nb"}}]}}`,
		`{"table":{"content":{"rows":[{"rowId":"r","cells":[{"cellId":"c","content":[{"text":{"text":"a\nb"}}]}]}]}}}`,
		`{"content":[{"text":{"text":"a\nb"}}]}`,
		`{"content":{"rows":[{"rowId":"r","cells":[{"cellId":"c","content":[{"link":{"href":"/","content":[{"text":"\r\na\n"}]}}]}]}]}}`,
	} {
		normalized, err := CanonicalizeStorageLocale(json.RawMessage(input))
		require.NoError(t, err)
		require.NotContains(t, string(normalized), `\n`)
		require.NotContains(t, string(normalized), `\r`)
		require.Contains(t, string(normalized), `"hardBreak":{}`)
		again, err := CanonicalizeStorageLocale(normalized)
		require.NoError(t, err)
		require.Equal(t, normalized, again)
	}
	for _, input := range []string{`{"content":[{"text":{"text":"a\\nb"}}]}`, `{"content":"a\nb"}`, `{"props":{"source":"a\nb"}}`, ` { "content": [{"hardBreak":{}}] } `} {
		normalized, err := CanonicalizeStorageLocale(json.RawMessage(input))
		require.NoError(t, err)
		require.Equal(t, input, string(normalized))
	}
}
