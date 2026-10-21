package displaysizemigrate

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	blockID = "10000000-0000-4000-8000-000000000001"
	fileID  = "20000000-0000-4000-8000-000000000002"
)

func versionFixture(kind, fingerprint, sizeField string) string {
	document := map[string]any{"blockCatalogFingerprint": fingerprint, "locale": "en", "base": map[string]any{}, "localeOverlay": map[string]any{"locale": "en"}}
	if kind != "page" {
		document["profile"] = "RICH_TEXT_PROFILE_" + strings.ToUpper(kind)
		document["base"] = map[string]any{"nodes": []any{map[string]any{"block": map[string]any{"id": blockID, "paragraph": map[string]any{"props": map[string]any{sizeField: 60, "textAlignment": "TEXT_ALIGNMENT_CENTER"}}}, "placement": map[string]any{}}}}
		document["localeOverlay"] = map[string]any{"locale": "en", "blocks": []any{map[string]any{"blockId": blockID, "paragraph": map[string]any{"props": map[string]any{}, "content": []any{map[string]any{"text": map[string]any{"text": "previewWidth stays text"}}}}}}}
	}
	encoded, _ := json.Marshal(map[string]any{"schemaVersion": 1, "sourceLocale": "en", "title": "Title", "summary": nil, "document": document})
	return string(encoded)
}

func TestLiveSharedJSONRenamesTheFieldAndKeepsItsValue(t *testing.T) {
	before := `{"file":{"props":{"attachment":{"activeFileId":"` + fileID + `"},"previewWidth":47}}}`

	after, changed, err := LiveSharedJSON("post", "file", []byte(before))

	require.NoError(t, err)
	require.True(t, changed)
	require.JSONEq(t, `{"file":{"props":{"attachment":{"activeFileId":"`+fileID+`"},"displaySize":47}}}`, string(after))

	unchanged, changed, err := LiveSharedJSON("post", "file", after)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, string(after), string(unchanged))
}

func TestLiveSharedJSONRejectsAmbiguousOrInvalidBlocks(t *testing.T) {
	_, _, err := LiveSharedJSON("post", "file", []byte(`{"file":{"props":{"attachment":{"activeFileId":"`+fileID+`"},"previewWidth":47,"displaySize":60}}}`))
	require.ErrorContains(t, err, "both previewWidth and displaySize")

	_, _, err = LiveSharedJSON("post", "file", []byte(`{"file":{"props":{"attachment":{"activeFileId":"`+fileID+`"},"previewWidth":500}}}`))
	require.ErrorContains(t, err, "validate converted file block")
}

func TestSnapshotJSONMovesReviewedHistoryToTheTargetCatalog(t *testing.T) {
	for _, kind := range []string{"page", "post", "work"} {
		for fingerprint := range sourceCatalogs {
			t.Run(kind+"/"+fingerprint[:8], func(t *testing.T) {
				after, changed, err := SnapshotJSON([]byte(versionFixture(kind, fingerprint, legacyField)), kind)

				require.NoError(t, err)
				require.True(t, changed)
				require.Contains(t, string(after), `"blockCatalogFingerprint":"`+targetCatalog+`"`)
				if kind != "page" {
					require.Contains(t, string(after), `"displaySize":60`)
					require.Contains(t, string(after), `"text":"previewWidth stays text"`)
				}
				repeated, changed, err := SnapshotJSON(after, kind)
				require.NoError(t, err)
				require.False(t, changed)
				require.Equal(t, string(after), string(repeated))
			})
		}
	}
}

func TestSnapshotJSONRejectsUnreviewedOrMismatchedHistory(t *testing.T) {
	_, _, err := SnapshotJSON([]byte(versionFixture("post", strings.Repeat("0", 64), legacyField)), "post")
	require.ErrorContains(t, err, "unreviewed stored catalog fingerprint")

	_, _, err = SnapshotJSON([]byte(versionFixture("post", targetCatalog, legacyField)), "work")
	require.ErrorContains(t, err, "version profile or locale mismatch")
}

func TestRenameDeltaRejectsAnyOtherChange(t *testing.T) {
	before := []byte(`{"file":{"props":{"previewWidth":47,"textAlignment":"left"}}}`)

	require.NoError(t, validateRenameDelta(before, []byte(`{"file":{"props":{"displaySize":47,"textAlignment":"left"}}}`), false))
	require.Error(t, validateRenameDelta(before, []byte(`{"file":{"props":{"displaySize":48,"textAlignment":"left"}}}`), false))
	require.Error(t, validateRenameDelta(before, []byte(`{"file":{"props":{"displaySize":47,"textAlignment":"right"}}}`), false))
	require.Error(t, validateRenameDelta(before, []byte(`{"file":{"props":{"displaySize":47}}}`), false))
}
