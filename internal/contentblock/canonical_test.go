package contentblock

import (
	"encoding/json"
	"testing"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestSnapshotDigestMatchesGeneratedStorageVectors(t *testing.T) {
	empty := newAggregate(Document{Profile: "post"})
	emptyHash, err := snapshotDigest(empty)
	require.NoError(t, err)
	generatedEmptyHash, err := contentv1.ContentStorageCanonicalHash("post", nil)
	require.NoError(t, err)
	require.Equal(t, generatedEmptyHash, emptyHash)

	blockID := uuid.New()
	shared := &contentv1.RichTextBlockData{Value: &contentv1.RichTextBlockData_Paragraph{
		Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}},
	}}
	localized := &contentv1.RichTextBlockLocaleData{Value: &contentv1.RichTextBlockLocaleData_Paragraph{
		Paragraph: &contentv1.ParagraphBlockLocale{Props: &contentv1.ParagraphLocaleProps{}},
	}}
	sharedJSON, err := protojson.Marshal(shared)
	require.NoError(t, err)
	localizedJSON, err := protojson.Marshal(localized)
	require.NoError(t, err)

	state := newAggregate(Document{Profile: "post"})
	state.blocks[blockID] = FullBlock{BaseBlock: BaseBlock{
		ID: blockID, ContainerSlot: "content", Kind: "paragraph", SharedData: sharedJSON,
	}}
	state.locales[blockID] = map[string]json.RawMessage{"en": localizedJSON}
	actual, err := snapshotDigest(state)
	require.NoError(t, err)

	generated, err := contentv1.ContentStorageCanonicalHash("post", []contentv1.ContentStorageRow{{
		BlockID: blockID.String(), ContainerSlot: "content", Kind: "paragraph", SharedData: sharedJSON,
		Locales: []contentv1.ContentStorageLocale{{Locale: "en", LocalizedData: localizedJSON}},
	}})
	require.NoError(t, err)
	require.Equal(t, generated, actual)
}

func TestSameCanonicalJSONComparesNestedObjectsSemantically(t *testing.T) {
	left := json.RawMessage(`{
		"paragraph": {
			"props": {"align": "left", "indent": 1},
			"content": [{"text": {"marks": {"italic": false, "bold": true}, "text": "hello"}}]
		}
	}`)
	right := json.RawMessage(`{"paragraph":{"content":[{"text":{"text":"hello","marks":{"bold":true,"italic":false}}}],"props":{"indent":1,"align":"left"}}}`)

	require.True(t, sameCanonicalJSON(left, right))
}
