package aidocumentadapter

import (
	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/richtextinline"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"testing"
)

func TestAIDocumentLineBreakExportImport(t *testing.T) {
	for _, text := range []string{"\na\n\nb\n", "\ra\r\n\nb\r"} {
		input, err := inlineToGenerated([]core.InlineItem{core.Bold(core.InlineText(text)), core.Link("https://example.test", core.Italic(core.InlineText("c\nd"))), core.HardBreak(), core.SiteVariable("site.title", false)})
		require.NoError(t, err)
		block := &contentv1.ParagraphBlockLocale{Content: input}
		field := block.ProtoReflect().Descriptor().Fields().ByName("content")
		exported, err := projectInlineList(block.ProtoReflect().Get(field).List())
		require.NoError(t, err)
		imported, err := inlineToGenerated(exported)
		require.NoError(t, err)
		require.Len(t, imported, len(input))
		for index := range input {
			require.True(t, proto.Equal(input[index], imported[index]))
		}
		require.Equal(t, input, richtextinline.Canonicalize(input))
	}
	// Legacy stored text is exported using the canonical AI br vocabulary.
	legacy := &contentv1.ParagraphBlockLocale{Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: "a\nb"}}}}}
	field := legacy.ProtoReflect().Descriptor().Fields().ByName("content")
	exported, err := projectInlineList(legacy.ProtoReflect().Get(field).List())
	require.NoError(t, err)
	require.Equal(t, []core.InlineItem{core.InlineText("a"), core.HardBreak(), core.InlineText("b")}, exported)
}
