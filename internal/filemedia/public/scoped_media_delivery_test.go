package public_test

import (
	"testing"
	"time"

	optional "github.com/echovisionlab/geul-api/internal/optional"

	. "github.com/echovisionlab/geul-api/internal/filemedia/public"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const unitMediaSigningSecret = "unit-media-signing-secret"

func ExportProjectScopedMediaURLPublishesReadyDerivativesForDrafts(t *testing.T) {
	imageID := "11111111-1111-4111-8111-111111111111"
	imageAssetID := "22222222-2222-4222-8222-222222222222"
	audioID := "33333333-3333-4333-8333-333333333333"
	hlsID := "44444444-4444-4444-8444-444444444444"
	spectrogramID := "55555555-5555-4555-8555-555555555555"
	waveformID := "66666666-6666-4666-8666-666666666666"

	t.Run("ready image projection is stable even for a direct draft", func(t *testing.T) {
		got, err := ExportProjectScopedMediaURL(
			"https://cdn.example.com",
			"https://media.example.com",
			unitMediaSigningSecret,
			TestMediaAccess{DirectDraft: true},
			MediaFile{ID: imageID, Extension: "png", MimeType: "image/png", FileSize: 123},
			nil,
			nil,
			nil,
			testReadyPublicAssetRow(imageAssetID, imageID, "image", "png", "image/png"),
		)
		require.NoError(t, err)
		assert.Equal(t, "https://cdn.example.com/asset/"+imageAssetID+"/image.png", optional.Value(optional.Value(got).Asset).URL)
		assert.Equal(t, optional.Value(got).Asset, optional.Value(got).Thumbnail)
		assert.Nil(t, optional.Value(got).Inline)
		assert.Nil(t, got.DurationSeconds, "unknown durations must remain absent")
	})

	t.Run("ready HLS waveform and spectrogram are stable even for a direct draft", func(t *testing.T) {
		hlsGenerationID := hlsID
		spectrogramAssetID := spectrogramID
		waveformAssetID := waveformID
		got, err := ExportProjectScopedMediaURL(
			"https://cdn.example.com",
			"https://media.example.com",
			unitMediaSigningSecret,
			TestMediaAccess{DirectDraft: true},
			MediaFile{ID: audioID, Extension: "flac", MimeType: "audio/flac", FileSize: 456, DurationSeconds: new(123)},
			map[string]Derivative{
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
			map[string]ReadyAsset{
				spectrogramID: testReadyPublicAssetRow(spectrogramID, audioID, "spectrogram", "webp", "image/webp"),
				waveformID:    testReadyPublicAssetRow(waveformID, audioID, "waveform", "json", "application/json"),
			},
			map[string]ReadyGeneration{
				hlsID: {FileID: audioID, GenerationID: hlsID, ManifestName: "master.m3u8"},
			},
			ReadyAsset{},
		)
		require.NoError(t, err)
		assert.Equal(t, "https://media.example.com/media/"+audioID+"/hls/"+hlsID+"/master.m3u8", optional.Value(optional.Value(got).Playback).URL)
		assert.Equal(t, hlsID, optional.Value(optional.Value(got).Playback).GenerationID)
		assert.EqualValues(t, 123, optional.Value(optional.Value(got).DurationSeconds))
		assert.Equal(t, "https://media.example.com/asset/"+spectrogramID+"/spectrogram.webp", optional.Value(optional.Value(got).Spectrogram).URL)
		assert.Equal(t, "https://media.example.com/asset/"+waveformID+"/waveform.json", optional.Value(optional.Value(got).Waveform).URL)
	})
}

func ExportProjectScopedMediaURLKeepsPrivateOriginalInline(t *testing.T) {
	fileID := "11111111-1111-4111-8111-111111111111"
	before := time.Now().UTC()
	got, err := ExportProjectScopedMediaURL(
		"https://cdn.example.com",
		"https://media.example.com",
		unitMediaSigningSecret,
		TestMediaAccess{DirectDraft: true},
		MediaFile{ID: fileID, Extension: "glb", MimeType: "model/gltf-binary", FileSize: 123},
		nil,
		nil,
		nil,
		ReadyAsset{},
	)
	require.NoError(t, err)
	require.NotNil(t, optional.Value(got).Inline)
	assert.Contains(t, optional.Value(optional.Value(got).Inline).URL, "/media/")
	assert.WithinDuration(t, before.Add(5*time.Minute), optional.Value(optional.Value(got).Inline).ExpiresAt, 2*time.Second)
	assert.Nil(t, optional.Value(got).Download)
}

func ExportProjectScopedMediaURLRejectsInvalidAuthority(t *testing.T) {
	fileID := "11111111-1111-4111-8111-111111111111"
	generationID := "22222222-2222-4222-8222-222222222222"

	_, err := ExportProjectScopedMediaURL(
		"https://cdn.example.com", "https://media.example.com", unitMediaSigningSecret, TestMediaAccess{},
		MediaFile{ID: fileID, Extension: "jpg", MimeType: "image/png", FileSize: 123},
		nil, nil, nil, ReadyAsset{},
	)
	require.EqualError(t, err, "file extension does not match MIME type")

	_, err = ExportProjectScopedMediaURL(
		"https://cdn.example.com", "https://media.example.com", unitMediaSigningSecret, TestMediaAccess{},
		MediaFile{ID: fileID, Extension: "flac", MimeType: "audio/flac", FileSize: 123},
		map[string]Derivative{
			managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String(): {MediaGenerationID: &generationID},
		},
		nil,
		map[string]ReadyGeneration{generationID: {FileID: "33333333-3333-4333-8333-333333333333", GenerationID: generationID, ManifestName: "master.m3u8"}},
		ReadyAsset{},
	)
	require.EqualError(t, err, "media generation does not belong to requested file")
}

func ExportPublicMediaSupportsInlineSourceUnit(t *testing.T) {
	t.Parallel()

	assert.True(t, ExportPublicMediaSupportsInlineSource("image/webp"))
	assert.True(t, ExportPublicMediaSupportsInlineSource(" model/gltf-binary "))
	assert.False(t, ExportPublicMediaSupportsInlineSource("audio/flac"))
	assert.False(t, ExportPublicMediaSupportsInlineSource("application/pdf"))
}

func testStringPointerOrNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func testReadyPublicAssetRow(assetID string, sourceFileID string, kind string, extension string, mimeType string) ReadyAsset {
	return ReadyAsset{
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
