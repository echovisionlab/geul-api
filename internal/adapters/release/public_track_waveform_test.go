package release

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTrackDerivativeURLFileIDNormalizationUnit(t *testing.T) {
	t.Parallel()

	got := uniqueNonEmptyIDs([]string{"", "file-a", "file-b", "file-a", "", "file-c", "file-b"})

	assert.Equal(t, []string{"file-a", "file-b", "file-c"}, got)
	assert.Empty(t, uniqueNonEmptyIDs(nil))
}

func TestProjectTrackStaticDerivativeRefsUnit(t *testing.T) {
	t.Parallel()

	waveformID := "11111111-1111-4111-8111-111111111111"
	spectrogramID := "22222222-2222-4222-8222-222222222222"

	got, err := projectTrackStaticDerivativeRefs("https://media.example.com", []trackStaticDerivativeRow{
		{
			FileID: "audio-1",
			Asset:  readyTrackTestAssetRow(waveformID, "audio-1", "waveform", "json", "application/json"),
		},
		{
			FileID: "audio-2",
			Asset:  readyTrackTestAssetRow(spectrogramID, "audio-2", "spectrogram", "webp", "image/webp"),
		},
	})
	require.NoError(t, err)

	require.Len(t, got, 2)
	require.NotNil(t, got["audio-1"])
	assert.Equal(t, waveformID, got["audio-1"].GetAssetId())
	assert.Equal(t, "https://media.example.com/asset/"+waveformID+"/waveform.json", got["audio-1"].GetUrl())
	require.NotNil(t, got["audio-2"])
	assert.Equal(t, spectrogramID, got["audio-2"].GetAssetId())
	assert.Equal(t, "https://media.example.com/asset/"+spectrogramID+"/spectrogram.webp", got["audio-2"].GetUrl())
	empty, err := projectTrackStaticDerivativeRefs("https://media.example.com", nil)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

func readyTrackTestAssetRow(assetID, sourceFileID, kind, extension, mimeType string) readyPublicAssetRow {
	return readyPublicAssetRow{
		AssetID:      assetID,
		SourceFileID: &sourceFileID,
		Kind:         kind,
		Extension:    extension,
		MimeType:     mimeType,
		FileSize:     123,
		SHA256:       make([]byte, 32),
		Disposition:  "inline",
	}
}

func TestProjectTrackHLSRefsUnit(t *testing.T) {
	t.Parallel()

	fileID := "33333333-3333-4333-8333-333333333333"
	generationID := "44444444-4444-4444-8444-444444444444"
	got, err := projectTrackHLSRefs("https://media.example.com", []trackHLSDerivativeRow{
		{
			FileID:       fileID,
			GenerationID: generationID,
			ManifestName: "master.m3u8",
		},
	})
	require.NoError(t, err)

	require.Len(t, got, 1)
	require.NotNil(t, got[fileID])
	assert.Equal(t, "https://media.example.com/media/"+fileID+"/hls/"+generationID+"/master.m3u8", got[fileID].GetUrl())
	assert.Equal(t, generationID, got[fileID].GetGenerationId())
	empty, err := projectTrackHLSRefs("https://media.example.com", nil)
	require.NoError(t, err)
	assert.Empty(t, empty)
}
