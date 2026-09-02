package mediaasset

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseHLSPrefixFromObjectKey(t *testing.T) {
	t.Parallel()
	fileID := "11111111-1111-4111-8111-111111111111"
	generationID := "22222222-2222-4222-8222-222222222222"
	prefixValue := "media/" + fileID + "/hls/" + generationID

	prefix, parsedFileID, parsedGenerationID, ok := ParseHLSPrefixFromObjectKey(prefixValue + "/segment_000.ts")
	require.True(t, ok)
	assert.Equal(t, prefixValue, prefix)
	assert.Equal(t, fileID, parsedFileID)
	assert.Equal(t, generationID, parsedGenerationID)

	prefix, parsedFileID, parsedGenerationID, ok = ParseHLSPrefixFromObjectKey(prefixValue + "/720p/segment_001.ts")
	require.True(t, ok)
	assert.Equal(t, prefixValue, prefix)
	assert.Equal(t, fileID, parsedFileID)
	assert.Equal(t, generationID, parsedGenerationID)

	_, _, _, ok = ParseHLSPrefixFromObjectKey("post/post-1/hls/file-1/segment.ts")
	assert.False(t, ok)
	_, _, _, ok = ParseHLSPrefixFromObjectKey("media/not-a-uuid/hls/not-a-uuid/segment.ts")
	assert.False(t, ok)
}

func TestSelectOrphanHLSPrefixesForCleanup(t *testing.T) {
	t.Parallel()

	cutoff := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	old := cutoff.Add(-time.Minute)
	recent := cutoff.Add(time.Minute)
	fileID := "11111111-1111-4111-8111-111111111111"
	generationID := "22222222-2222-4222-8222-222222222222"
	prefixValue := "media/" + fileID + "/hls/" + generationID

	tests := []struct {
		name             string
		inventory        []HLSPrefixInventory
		referenced       map[string]struct{}
		active           map[string]struct{}
		wantDeletable    []string
		wantInconsistent []string
	}{
		{
			name: "selects old prefix without manifest reference or active job",
			inventory: []HLSPrefixInventory{
				{
					Prefix:         prefixValue,
					FileID:         fileID,
					GenerationID:   generationID,
					LatestModified: old,
				},
			},
			wantDeletable: []string{prefixValue},
		},
		{
			name: "reports old prefix whose missing manifest is still referenced",
			inventory: []HLSPrefixInventory{
				{
					Prefix:         prefixValue,
					FileID:         fileID,
					GenerationID:   generationID,
					LatestModified: old,
				},
			},
			referenced: map[string]struct{}{
				generationID: {},
			},
			wantInconsistent: []string{prefixValue},
		},
		{
			name: "skips prefix for active transcode file",
			inventory: []HLSPrefixInventory{
				{
					Prefix:         prefixValue,
					FileID:         fileID,
					GenerationID:   generationID,
					LatestModified: old,
				},
			},
			active: map[string]struct{}{
				fileID: {},
			},
		},
		{
			name: "skips recent prefix",
			inventory: []HLSPrefixInventory{
				{
					Prefix:         prefixValue,
					FileID:         fileID,
					GenerationID:   generationID,
					LatestModified: recent,
				},
			},
		},
		{
			name: "skips prefix with manifest object",
			inventory: []HLSPrefixInventory{
				{
					Prefix:         prefixValue,
					FileID:         fileID,
					GenerationID:   generationID,
					LatestModified: old,
					HasManifest:    true,
				},
			},
		},
		{
			name: "skips prefix without file id",
			inventory: []HLSPrefixInventory{
				{
					Prefix:         "media//hls/",
					LatestModified: old,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			deletable, inconsistent := SelectOrphanHLSPrefixesForCleanup(tt.inventory, tt.referenced, tt.active, cutoff)

			assert.Equal(t, tt.wantDeletable, hlsInventoryPrefixes(deletable))
			assert.Equal(t, tt.wantInconsistent, hlsInventoryPrefixes(inconsistent))
		})
	}
}

func hlsInventoryPrefixes(inventory []HLSPrefixInventory) []string {
	if len(inventory) == 0 {
		return nil
	}

	prefixes := make([]string, 0, len(inventory))
	for _, item := range inventory {
		prefixes = append(prefixes, item.Prefix)
	}
	return prefixes
}
