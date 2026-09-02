package public

import (
	"testing"
	"time"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const unitMediaSigningSecret = "unit-media-signing-secret"

func TestProjectScopedMediaURLPublishesReadyDerivativesForDrafts(t *testing.T) {
	imageID := "11111111-1111-4111-8111-111111111111"
	imageAssetID := "22222222-2222-4222-8222-222222222222"
	audioID := "33333333-3333-4333-8333-333333333333"
	hlsID := "44444444-4444-4444-8444-444444444444"
	spectrogramID := "55555555-5555-4555-8555-555555555555"
	waveformID := "66666666-6666-4666-8666-666666666666"

	t.Run("ready image projection is stable even for a direct draft", func(t *testing.T) {
		got, err := projectScopedMediaURL(
			"https://cdn.example.com",
			"https://media.example.com",
			unitMediaSigningSecret,
			resolvedMediaAccess{directDraft: true},
			scopedMediaFile{ID: imageID, Extension: "png", MimeType: "image/png", FileSize: 123},
			nil,
			nil,
			nil,
			testReadyPublicAssetRow(imageAssetID, imageID, "image", "png", "image/png"),
		)
		require.NoError(t, err)
		assert.Equal(t, "https://cdn.example.com/asset/"+imageAssetID+"/image.png", got.GetAsset().GetUrl())
		assert.Equal(t, got.GetAsset(), got.GetThumbnail())
		assert.Nil(t, got.GetInline())
	})

	t.Run("ready HLS waveform and spectrogram are stable even for a direct draft", func(t *testing.T) {
		hlsGenerationID := hlsID
		spectrogramAssetID := spectrogramID
		waveformAssetID := waveformID
		got, err := projectScopedMediaURL(
			"https://cdn.example.com",
			"https://media.example.com",
			unitMediaSigningSecret,
			resolvedMediaAccess{directDraft: true},
			scopedMediaFile{ID: audioID, Extension: "flac", MimeType: "audio/flac", FileSize: 456},
			map[string]scopedMediaDerivative{
				managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String(): {
					FileID: audioID, Type: managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String(), MediaGenerationID: &hlsGenerationID,
				},
				managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_SPECTROGRAM.String(): {
					FileID: audioID, Type: managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_SPECTROGRAM.String(), AssetID: &spectrogramAssetID,
				},
				managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM.String(): {
					FileID: audioID, Type: managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM.String(), AssetID: &waveformAssetID,
				},
			},
			map[string]readyPublicAssetRow{
				spectrogramID: testReadyPublicAssetRow(spectrogramID, audioID, "spectrogram", "webp", "image/webp"),
				waveformID:    testReadyPublicAssetRow(waveformID, audioID, "waveform", "json", "application/json"),
			},
			map[string]readyMediaGenerationRow{
				hlsID: {FileID: audioID, GenerationID: hlsID, ManifestName: "master.m3u8"},
			},
			readyPublicAssetRow{},
		)
		require.NoError(t, err)
		assert.Equal(t, "https://media.example.com/media/"+audioID+"/hls/"+hlsID+"/master.m3u8", got.GetPlayback().GetUrl())
		assert.Equal(t, hlsID, got.GetPlayback().GetGenerationId())
		assert.Equal(t, "https://media.example.com/asset/"+spectrogramID+"/spectrogram.webp", got.GetSpectrogram().GetUrl())
		assert.Equal(t, "https://media.example.com/asset/"+waveformID+"/waveform.json", got.GetWaveform().GetUrl())
	})
}

func TestProjectScopedMediaURLKeepsPrivateOriginalInline(t *testing.T) {
	fileID := "11111111-1111-4111-8111-111111111111"
	before := time.Now().UTC()
	got, err := projectScopedMediaURL(
		"https://cdn.example.com",
		"https://media.example.com",
		unitMediaSigningSecret,
		resolvedMediaAccess{directDraft: true},
		scopedMediaFile{ID: fileID, Extension: "glb", MimeType: "model/gltf-binary", FileSize: 123},
		nil,
		nil,
		nil,
		readyPublicAssetRow{},
	)
	require.NoError(t, err)
	require.NotNil(t, got.GetInline())
	assert.Contains(t, got.GetInline().GetUrl(), "/media/")
	assert.WithinDuration(t, before.Add(maxDirectDraftMediaTTL), got.GetInline().GetExpiresAt().AsTime(), 2*time.Second)
	assert.Nil(t, got.GetDownload())
}

func TestProjectScopedMediaURLRejectsInvalidAuthority(t *testing.T) {
	fileID := "11111111-1111-4111-8111-111111111111"
	generationID := "22222222-2222-4222-8222-222222222222"

	_, err := projectScopedMediaURL(
		"https://cdn.example.com", "https://media.example.com", unitMediaSigningSecret, resolvedMediaAccess{},
		scopedMediaFile{ID: fileID, Extension: "jpg", MimeType: "image/png", FileSize: 123},
		nil, nil, nil, readyPublicAssetRow{},
	)
	require.EqualError(t, err, "file extension does not match MIME type")

	_, err = projectScopedMediaURL(
		"https://cdn.example.com", "https://media.example.com", unitMediaSigningSecret, resolvedMediaAccess{},
		scopedMediaFile{ID: fileID, Extension: "flac", MimeType: "audio/flac", FileSize: 123},
		map[string]scopedMediaDerivative{
			managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String(): {MediaGenerationID: &generationID},
		},
		nil,
		map[string]readyMediaGenerationRow{generationID: {FileID: "33333333-3333-4333-8333-333333333333", GenerationID: generationID, ManifestName: "master.m3u8"}},
		readyPublicAssetRow{},
	)
	require.EqualError(t, err, "media generation does not belong to requested file")
}

func TestPublicMediaSupportsInlineSourceUnit(t *testing.T) {
	t.Parallel()

	assert.True(t, publicMediaSupportsInlineSource("image/webp"))
	assert.True(t, publicMediaSupportsInlineSource(" model/gltf-binary "))
	assert.False(t, publicMediaSupportsInlineSource("audio/flac"))
	assert.False(t, publicMediaSupportsInlineSource("application/pdf"))
}

func testStringPointerOrNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func testReadyPublicAssetRow(assetID string, sourceFileID string, kind string, extension string, mimeType string) readyPublicAssetRow {
	return readyPublicAssetRow{
		AssetID:      assetID,
		SourceFileID: testStringPointerOrNil(sourceFileID),
		Kind:         kind,
		Extension:    extension,
		MimeType:     mimeType,
		FileSize:     123,
		SHA256:       make([]byte, 32),
		Disposition:  "inline",
	}
}
