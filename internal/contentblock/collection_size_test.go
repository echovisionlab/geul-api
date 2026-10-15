package contentblock

import (
	"encoding/json"
	"testing"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

// Exercise the same protobuf JSON accepted from Page room writes, its canonical
// storage rows, and the public renderer projection for every collection kind.
func TestCollectionSizeSurvivesPageJSONAndStorageRoundTrip(t *testing.T) {
	for _, kind := range []string{"postList", "workList", "programEventList", "releaseList", "artistList", "labelList", "authorList"} {
		for _, size := range []string{"", "ITEM_SIZE_S", "ITEM_SIZE_M", "ITEM_SIZE_L"} {
			t.Run(kind+"/"+size, func(t *testing.T) {
				sectionID := uuid.New().String()
				inputProps := map[string]any{}
				if size != "" {
					inputProps["itemSize"] = size
				}
				raw, err := json.Marshal(map[string]any{
					"blockCatalogFingerprint": contentv1.ContentBlockCatalogFingerprint,
					"sourceLocale":            "en",
					"base": map[string]any{"nodes": []any{map[string]any{
						"section":   map[string]any{"id": sectionID, kind: map[string]any{"props": inputProps}},
						"placement": map[string]any{"index": 0},
					}}},
					"localeOverlays": []any{map[string]any{
						"locale":   "en",
						"sections": []any{map[string]any{"sectionId": sectionID, kind: map[string]any{"props": map[string]any{}}}},
					}},
				})
				require.NoError(t, err)
				document := &contentv1.PageDocument{}
				require.NoError(t, protojson.Unmarshal(raw, document))
				documentID, revision := uuid.New(), uuid.New()
				replace, err := replaceFromPageProtoForTest(documentID, revision, document)
				require.NoError(t, err)
				projection, err := MaterializeSnapshotPageLocale(Snapshot{
					Document:     Document{ID: documentID, Profile: "page", Revision: revision},
					SourceLocale: "en", Blocks: replace.Blocks, LocaleOverlays: replace.LocaleOverlays,
				}, "en")
				require.NoError(t, err)
				encoded, err := protojson.Marshal(projection)
				require.NoError(t, err)
				var result map[string]any
				require.NoError(t, json.Unmarshal(encoded, &result))
				nodes := result["base"].(map[string]any)["nodes"].([]any)
				section := nodes[0].(map[string]any)["section"].(map[string]any)
				props := section[kind].(map[string]any)["props"].(map[string]any)
				if size == "" || size == "ITEM_SIZE_M" {
					// Canonical storage omits descriptor defaults. The renderer schema
					// applies the shared m default to these legacy/default payloads.
					require.NotContains(t, props, "itemSize")
				} else {
					require.Equal(t, size, props["itemSize"])
				}
			})
		}
	}
}
