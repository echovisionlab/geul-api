package transcodetest

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/mediaauth"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// AudioJob is a canonical audio transcode command of fileID.
func AudioJob(t *testing.T, fileID string) *managev1.TranscodeAudioEvent {
	t.Helper()
	generationID := uuid.NewString()
	spectrogramAssetID := uuid.NewString()
	sourceKey, err := mediaauth.MediaObjectKey(fileID, "ogg")
	require.NoError(t, err)
	hlsPrefix, err := mediaauth.MediaHLSObjectPrefix(fileID, generationID)
	require.NoError(t, err)
	spectrogramKey, err := mediaauth.AssetObjectKey(spectrogramAssetID, "png")
	require.NoError(t, err)
	return &managev1.TranscodeAudioEvent{
		EventId:    uuid.NewString(),
		EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		EntityId:   uuid.NewString(),
		FileId:     fileID,
		Source: &commonv1.MediaObjectTarget{
			FileId: fileID, ObjectKey: sourceKey, Extension: "ogg", MimeType: "audio/ogg",
		},
		HlsOutput: &commonv1.MediaGenerationWriteTarget{
			GenerationId: generationID, FileId: fileID, ObjectPrefix: hlsPrefix,
		},
		SpectrogramOutput: &commonv1.AssetWriteTarget{
			AssetId: spectrogramAssetID, ObjectKey: spectrogramKey, Extension: "png",
			MimeType: "image/png", Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
		},
	}
}

// VideoJob is a canonical video transcode command of fileID.
func VideoJob(t *testing.T, fileID string) *managev1.TranscodeVideoEvent {
	t.Helper()
	generationID := uuid.NewString()
	thumbnailAssetID := uuid.NewString()
	sourceKey, err := mediaauth.MediaObjectKey(fileID, "mp4")
	require.NoError(t, err)
	hlsPrefix, err := mediaauth.MediaHLSObjectPrefix(fileID, generationID)
	require.NoError(t, err)
	thumbnailKey, err := mediaauth.AssetObjectKey(thumbnailAssetID, "webp")
	require.NoError(t, err)
	return &managev1.TranscodeVideoEvent{
		EventId: uuid.NewString(), EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		EntityId: uuid.NewString(), FileId: fileID,
		Source: &commonv1.MediaObjectTarget{
			FileId: fileID, ObjectKey: sourceKey, Extension: "mp4", MimeType: "video/mp4",
		},
		HlsOutput: &commonv1.MediaGenerationWriteTarget{
			GenerationId: generationID, FileId: fileID, ObjectPrefix: hlsPrefix,
		},
		ThumbnailOutput: &commonv1.AssetWriteTarget{
			AssetId: thumbnailAssetID, ObjectKey: thumbnailKey, Extension: "webp",
			MimeType: "image/webp", Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
		},
	}
}

// NopPublisher accepts every command and publishes nothing.
type NopPublisher struct{}

func (NopPublisher) PublishTranscodeAudio(context.Context, *managev1.TranscodeAudioEvent) error {
	return nil
}

func (NopPublisher) PublishTranscodeVideo(context.Context, *managev1.TranscodeVideoEvent) error {
	return nil
}

func (NopPublisher) PublishWaveformCancel(context.Context, *managev1.WaveformCancelEvent) error {
	return nil
}
