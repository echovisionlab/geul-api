package application

import (
	"bytes"
	"context"
	"errors"
	"testing"

	mediaauth "github.com/echovisionlab/geul-mediaauth"

	"github.com/echovisionlab/geul-api/internal/model"
	transcodestate "github.com/echovisionlab/geul-api/internal/transcode"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestTranscodeOutputIDsMatchTrackedAllocation(t *testing.T) {
	t.Parallel()
	eventID := randomWorkerTranscodeUUID()
	entityID := randomWorkerTranscodeUUID()
	fileID := randomWorkerTranscodeUUID()
	hlsID := randomWorkerTranscodeUUID()
	audioAssetID := randomWorkerTranscodeUUID()
	videoAssetID := randomWorkerTranscodeUUID()
	audioJob := &managev1.TranscodeAudioEvent{
		EventId: eventID, EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		EntityId: entityID, FileId: fileID,
		Source:            workerCanonicalSource(t, fileID, "ogg", "audio/ogg"),
		HlsOutput:         workerCanonicalGeneration(t, fileID, hlsID),
		SpectrogramOutput: workerCanonicalAsset(t, audioAssetID, "png", "image/png"),
	}
	audioPayload, err := proto.Marshal(audioJob)
	require.NoError(t, err)

	matches, err := transcodestate.MatchTranscodeCompletionPayload(&managev1.TranscodeCompleteEvent{
		EventType:  managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO,
		EntityType: audioJob.EntityType,
		EntityId:   audioJob.EntityId,
		FileId:     audioJob.FileId,
		Outputs: &managev1.TranscodeOutputs{
			Hls:         &commonv1.MediaGenerationWriteResult{GenerationId: hlsID},
			Spectrogram: &commonv1.AssetWriteResult{AssetId: audioAssetID},
		},
	}, audioPayload)
	require.NoError(t, err)
	require.True(t, matches)

	matches, err = transcodestate.MatchTranscodeCompletionPayload(&managev1.TranscodeCompleteEvent{
		EventType:  managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO,
		EntityType: audioJob.EntityType, EntityId: audioJob.EntityId, FileId: audioJob.FileId,
		Outputs: &managev1.TranscodeOutputs{
			Hls:         &commonv1.MediaGenerationWriteResult{GenerationId: hlsID},
			Spectrogram: &commonv1.AssetWriteResult{AssetId: randomWorkerTranscodeUUID()},
		},
	}, audioPayload)
	require.NoError(t, err)
	require.False(t, matches)

	videoJob := &managev1.TranscodeVideoEvent{
		EventId: randomWorkerTranscodeUUID(), EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		EntityId: entityID, FileId: fileID,
		Source:          workerCanonicalSource(t, fileID, "mp4", "video/mp4"),
		HlsOutput:       workerCanonicalGeneration(t, fileID, hlsID),
		ThumbnailOutput: workerCanonicalAsset(t, videoAssetID, "webp", "image/webp"),
	}
	videoPayload, err := proto.Marshal(videoJob)
	require.NoError(t, err)
	matches, err = transcodestate.MatchTranscodeCompletionPayload(&managev1.TranscodeCompleteEvent{
		EventType:  managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_VIDEO,
		EntityType: videoJob.EntityType, EntityId: videoJob.EntityId, FileId: videoJob.FileId,
		Outputs: &managev1.TranscodeOutputs{
			Hls:       &commonv1.MediaGenerationWriteResult{GenerationId: hlsID},
			Thumbnail: &commonv1.AssetWriteResult{AssetId: videoAssetID},
		},
	}, videoPayload)
	require.NoError(t, err)
	require.True(t, matches)

	matches, err = transcodestate.MatchTranscodeCompletionPayload(&managev1.TranscodeCompleteEvent{
		EventType:  managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_VIDEO,
		EntityType: videoJob.EntityType, EntityId: videoJob.EntityId, FileId: videoJob.FileId,
		Outputs: &managev1.TranscodeOutputs{
			Hls:       &commonv1.MediaGenerationWriteResult{GenerationId: randomWorkerTranscodeUUID()},
			Thumbnail: &commonv1.AssetWriteResult{AssetId: videoAssetID},
		},
	}, videoPayload)
	require.NoError(t, err)
	require.False(t, matches)
}

func TestTranscodeOutputIDsMatchRejectsMissingAndMalformedPayloads(t *testing.T) {
	t.Parallel()
	matches, err := transcodestate.MatchTranscodeCompletionPayload(nil, nil)
	require.NoError(t, err)
	require.False(t, matches)

	matches, err = transcodestate.MatchTranscodeCompletionPayload(&managev1.TranscodeCompleteEvent{}, nil)
	require.NoError(t, err)
	require.False(t, matches)

	for _, eventType := range []managev1.TranscodeEventType{
		managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO,
		managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_VIDEO,
	} {
		_, err = transcodestate.MatchTranscodeCompletionPayload(&managev1.TranscodeCompleteEvent{
			EventType: eventType,
			Outputs:   &managev1.TranscodeOutputs{},
		}, []byte{0xff})
		require.Error(t, err)
	}

	matches, err = transcodestate.MatchTranscodeCompletionPayload(&managev1.TranscodeCompleteEvent{
		EventType: managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_UNSPECIFIED,
		Outputs:   &managev1.TranscodeOutputs{},
	}, nil)
	require.NoError(t, err)
	require.False(t, matches)
}

func TestFinishTranscodeJobDefersToTracker(t *testing.T) {
	t.Parallel()
	event := &managev1.TranscodeCompleteEvent{FileId: randomWorkerTranscodeUUID()}
	require.NoError(t, (&Handlers{}).finishTranscodeJob(context.Background(), event))

	wantErr := errors.New("tracker unavailable")
	tracker := &unitTranscodeJobTracker{completeErr: wantErr}
	err := (&Handlers{transcodeJobs: tracker}).finishTranscodeJob(context.Background(), event)
	require.ErrorIs(t, err, wantErr)
	require.Equal(t, event, tracker.completeEvent)
}

func TestWaveformJobMatchesEvent(t *testing.T) {
	t.Parallel()
	job := &model.WaveformJob{
		EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST.String(),
		EntityID:   "entity",
		FileID:     "file",
	}
	require.True(t, waveformJobMatchesEvent(job, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST, "entity", "file"))
	require.False(t, waveformJobMatchesEvent(nil, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST, "entity", "file"))
	require.False(t, waveformJobMatchesEvent(job, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE, "entity", "file"))
	require.False(t, waveformJobMatchesEvent(job, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST, "other", "file"))
	require.False(t, waveformJobMatchesEvent(job, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST, "entity", "other"))
}

func TestValidateTranscodeCompletionOutputs(t *testing.T) {
	t.Parallel()
	validHLS := func() *commonv1.MediaGenerationWriteResult {
		return &commonv1.MediaGenerationWriteResult{
			ManifestSha256: bytes.Repeat([]byte{0x11}, 32), ObjectCount: 1, TotalSize: 1,
		}
	}
	validAsset := func() *commonv1.AssetWriteResult {
		return &commonv1.AssetWriteResult{FileSize: 1, Sha256: bytes.Repeat([]byte{0x22}, 32)}
	}
	require.Error(t, validateTranscodeCompletionOutputs(nil))
	require.Error(t, validateTranscodeCompletionOutputs(&managev1.TranscodeCompleteEvent{}))
	require.ErrorContains(t, validateTranscodeCompletionOutputs(&managev1.TranscodeCompleteEvent{
		Outputs: &managev1.TranscodeOutputs{},
	}), "HLS")
	require.ErrorContains(t, validateTranscodeCompletionOutputs(&managev1.TranscodeCompleteEvent{
		EventType: managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO,
		Outputs:   &managev1.TranscodeOutputs{Hls: validHLS()},
	}), "spectrogram")
	require.ErrorContains(t, validateTranscodeCompletionOutputs(&managev1.TranscodeCompleteEvent{
		EventType: managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_VIDEO,
		Outputs:   &managev1.TranscodeOutputs{Hls: validHLS()},
	}), "thumbnail")
	require.ErrorContains(t, validateTranscodeCompletionOutputs(&managev1.TranscodeCompleteEvent{
		EventType: managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_UNSPECIFIED,
		Outputs:   &managev1.TranscodeOutputs{Hls: validHLS()},
	}), "event_type")
	require.NoError(t, validateTranscodeCompletionOutputs(&managev1.TranscodeCompleteEvent{
		EventType: managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO,
		Outputs: &managev1.TranscodeOutputs{
			Hls: validHLS(), Spectrogram: validAsset(),
		},
	}))
	require.NoError(t, validateTranscodeCompletionOutputs(&managev1.TranscodeCompleteEvent{
		EventType: managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_VIDEO,
		Outputs: &managev1.TranscodeOutputs{
			Hls: validHLS(), Thumbnail: validAsset(),
		},
	}))
}

func randomWorkerTranscodeUUID() string {
	return uuid.NewString()
}

func workerCanonicalSource(t *testing.T, fileID string, extension string, mimeType string) *commonv1.MediaObjectTarget {
	t.Helper()
	objectKey, err := mediaauth.MediaObjectKey(fileID, extension)
	require.NoError(t, err)
	return &commonv1.MediaObjectTarget{
		FileId: fileID, ObjectKey: objectKey, Extension: extension, MimeType: mimeType,
	}
}

func workerCanonicalGeneration(t *testing.T, fileID string, generationID string) *commonv1.MediaGenerationWriteTarget {
	t.Helper()
	objectPrefix, err := mediaauth.MediaHLSObjectPrefix(fileID, generationID)
	require.NoError(t, err)
	return &commonv1.MediaGenerationWriteTarget{
		GenerationId: generationID, FileId: fileID, ObjectPrefix: objectPrefix,
	}
}

func workerCanonicalAsset(t *testing.T, assetID string, extension string, mimeType string) *commonv1.AssetWriteTarget {
	t.Helper()
	objectKey, err := mediaauth.AssetObjectKey(assetID, extension)
	require.NoError(t, err)
	return &commonv1.AssetWriteTarget{
		AssetId: assetID, ObjectKey: objectKey, Extension: extension, MimeType: mimeType,
		Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	}
}

type unitTranscodeJobTracker struct {
	completeEvent *managev1.TranscodeCompleteEvent
	completeErr   error
}

func (*unitTranscodeJobTracker) HandleTranscodeProgress(context.Context, *managev1.TranscodeProgressEvent) error {
	return nil
}

func (t *unitTranscodeJobTracker) HandleTranscodeComplete(_ context.Context, event *managev1.TranscodeCompleteEvent) error {
	t.completeEvent = event
	return t.completeErr
}

func (*unitTranscodeJobTracker) MarkCancelled(context.Context, string, managev1.TranscodeCancelReason) error {
	return nil
}
