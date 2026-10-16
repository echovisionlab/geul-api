package contentblock

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestBlockRoomInlineJSONThroughProtoHTMLAndPlainText(t *testing.T) {
	// Same wire shape produced by Tiptap -> block room, including edge breaks,
	// consecutive breaks, styled text/link labels, and a paragraph boundary.
	var block contentv1.RichTextBlockLocale
	require.NoError(t, protojson.Unmarshal([]byte(`{"blockId":"10000000-0000-4000-8000-000000000111","paragraph":{"content":[{"hardBreak":{}},{"text":{"text":"a","styles":{"bold":true}}},{"hardBreak":{}},{"hardBreak":{}},{"link":{"href":"https://example.test","content":[{"text":"b","styles":{"italic":true}}]}},{"hardBreak":{}}]}}`), &block))
	rendered, err := renderInline(block.GetParagraph().GetContent())
	require.NoError(t, err)
	require.Equal(t, `<br><strong>a</strong><br><br><a href="https://example.test"><em>b</em></a><br>`, rendered.HTML)
	require.Equal(t, "\na\n\nb\n", rendered.Text)
	for _, legacy := range []string{"\na\r\n\nb\r", "\na\n\nb\n"} {
		rendered, err := renderInline([]*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: legacy}}}})
		require.NoError(t, err)
		require.Equal(t, "<br>a<br><br>b<br>", rendered.HTML)
		require.Equal(t, "\na\n\nb\n", rendered.Text)
	}
}

func TestStorageMutationCanonicalizesOnlySubmittedLocaleBlocks(t *testing.T) {
	const id = "10000000-0000-4000-8000-000000000111"
	raw := json.RawMessage(`{"paragraph":{"content":[{"text":{"text":"a\r\nb"}}]}}`)
	batch, err := batchFromStorage(uuid.MustParse(id), "post", contentv1.ContentStorageMutationBatch{
		ExpectedRevision: id,
		LocaleGroups:     []contentv1.ContentStorageLocaleMutationGroup{{Locale: "en", Upserts: []contentv1.ContentStorageLocaleUpsert{{BlockID: id, ExpectedKind: "paragraph", LocalizedData: raw}}}},
	})
	require.NoError(t, err)
	require.Len(t, batch.LocaleGroups, 1)
	require.Len(t, batch.LocaleGroups[0].Upserts, 1)
	require.JSONEq(t, `{"paragraph":{"content":[{"text":{"text":"a"}},{"hardBreak":{}},{"text":{"text":"b"}}]}}`, string(batch.LocaleGroups[0].Upserts[0].LocalizedData))
	require.Equal(t, `{"paragraph":{"content":[{"text":{"text":"a\r\nb"}}]}}`, string(raw))
	// Direct/internal batches also pass through the locale-only hot writer.
	batch.LocaleGroups[0].Upserts[0].LocalizedData = raw
	store := &Store{contract: NewGeneratedContract()}
	inputs, err := store.normalizeHotLocaleInputs(batch, "en")
	require.NoError(t, err)
	require.Len(t, inputs, 1)
	require.JSONEq(t, `{"paragraph":{"props":{},"content":[{"text":{"text":"a"}},{"hardBreak":{}},{"text":{"text":"b"}}]}}`, string(inputs[0].LocalizedData))
}
