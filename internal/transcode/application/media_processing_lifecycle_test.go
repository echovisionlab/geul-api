package application

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestMediaProcessingSnapshotAudioReadinessRequiresAllDerivatives(t *testing.T) {
	snapshot := mediaProcessingSnapshot{
		File: model.File{
			ID:       randomWorkerTranscodeUUID(),
			MimeType: "audio/ogg",
		},
		Derivatives: map[string]mediaProcessingDerivative{
			managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String(): {
				MediaGenerationID: optionalString("hls-generation"),
			},
			managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_SPECTROGRAM.String(): {
				AssetID: optionalString("spectrogram-asset"),
			},
		},
		HLSProgress:         100,
		SpectrogramProgress: 100,
		WaveformProgress:    42,
	}

	completedDerivatives, requiredDerivatives := snapshot.completedRequiredDerivatives()

	require.Equal(t, int32(2), completedDerivatives)
	require.Equal(t, int32(3), requiredDerivatives)
	require.Equal(t, int32(81), snapshot.aggregateProgress())
}

func TestMediaProcessingSnapshotReadyDerivativeDoesNotRegressFromLateProgress(t *testing.T) {
	snapshot := mediaProcessingSnapshot{
		File: model.File{
			ID:       randomWorkerTranscodeUUID(),
			MimeType: "audio/ogg",
		},
		Derivatives: map[string]mediaProcessingDerivative{
			managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String(): {
				MediaGenerationID: optionalString("hls-generation"),
			},
			managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_SPECTROGRAM.String(): {
				AssetID: optionalString("spectrogram-asset"),
			},
			managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM.String(): {
				AssetID: optionalString("waveform-asset"),
			},
		},
		HLSProgress:         40,
		SpectrogramProgress: 40,
		WaveformProgress:    40,
	}
	if snapshot.hasDerivative(managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS) {
		snapshot.HLSProgress = 100
	}
	if snapshot.hasDerivative(managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_SPECTROGRAM) {
		snapshot.SpectrogramProgress = 100
	}
	if snapshot.hasDerivative(managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM) {
		snapshot.WaveformProgress = 100
	}

	completedDerivatives, requiredDerivatives := snapshot.completedRequiredDerivatives()

	require.Equal(t, int32(3), completedDerivatives)
	require.Equal(t, int32(3), requiredDerivatives)
	require.Equal(t, int32(100), snapshot.aggregateProgress())
}

func TestMediaProcessingLifecycleStatusDoesNotInferReadyFromProgress(t *testing.T) {
	snapshot := mediaProcessingSnapshot{
		File: model.File{
			ID:       randomWorkerTranscodeUUID(),
			MimeType: "video/mp4",
		},
		Derivatives: map[string]mediaProcessingDerivative{},
		HLSProgress: 100,
	}

	require.Equal(
		t,
		commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_PROCESSING,
		snapshot.lifecycleStatus(),
	)
}

func TestMediaProcessingSnapshotVideoReadinessRequiresThumbnailAndDuration(t *testing.T) {
	duration := 1
	snapshot := mediaProcessingSnapshot{
		File: model.File{
			ID:              randomWorkerTranscodeUUID(),
			MimeType:        "video/mp4",
			DurationSeconds: &duration,
		},
		Derivatives: map[string]mediaProcessingDerivative{
			managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String(): {
				MediaGenerationID: optionalString("hls-generation"),
			},
		},
		HLSProgress: 100,
	}

	completedDerivatives, requiredDerivatives := snapshot.completedRequiredDerivatives()

	require.Equal(t, int32(1), completedDerivatives)
	require.Equal(t, int32(2), requiredDerivatives)
	require.Equal(
		t,
		commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_PROCESSING,
		snapshot.lifecycleStatus(),
	)

	snapshot.Derivatives[managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_THUMBNAIL.String()] = mediaProcessingDerivative{
		AssetID: optionalString("thumbnail-asset"),
	}

	require.Equal(
		t,
		commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_READY,
		snapshot.lifecycleStatus(),
	)
}

func TestMediaProcessingSnapshotVideoReadinessRequiresDuration(t *testing.T) {
	snapshot := mediaProcessingSnapshot{
		File: model.File{
			ID:       randomWorkerTranscodeUUID(),
			MimeType: "video/mp4",
		},
		Derivatives: map[string]mediaProcessingDerivative{
			managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String(): {
				MediaGenerationID: optionalString("hls-generation"),
			},
			managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_THUMBNAIL.String(): {
				AssetID: optionalString("thumbnail-asset"),
			},
		},
		HLSProgress: 100,
	}

	require.Equal(
		t,
		commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_PROCESSING,
		snapshot.lifecycleStatus(),
	)
}

func TestPlanMediaProcessingLifecycleEventBuildsProcessingEvent(t *testing.T) {
	slotID := "slot-1"
	attemptID := "attempt-1"
	snapshot := mediaProcessingSnapshot{
		File: model.File{
			ID:              "file-1",
			MimeType:        "audio/ogg",
			IngestSlotID:    &slotID,
			IngestAttemptID: &attemptID,
		},
		Derivatives:         map[string]mediaProcessingDerivative{},
		HLSProgress:         100,
		SpectrogramProgress: 30,
		WaveformProgress:    20,
	}

	got, err := (&Handlers{}).planMediaProcessingLifecycleEvent(
		mediaProcessingPublishOptions{
			EntityType:    managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK,
			EntityID:      "track-1",
			FileID:        "file-1",
			CorrelationID: "correlation-processing",
			Sequence:      11,
			TimestampMs:   12345,
		},
		mediaProcessingLifecycleIdentity{
			TrackID:   "track-1",
			ReleaseID: "release-1",
		},
		snapshot,
	)

	require.NoError(t, err)
	require.Equal(t, "correlation-processing", got.GetCorrelationId())
	require.Equal(t, commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_PROCESSING, got.GetStatus())
	require.Equal(t, int32(50), got.GetPercentage())
	require.Nil(t, got.Outputs)
	require.Nil(t, got.Error)
	require.Equal(t, "track-1", got.GetTrackId())
	require.Equal(t, "release-1", got.GetReleaseId())
	require.Equal(t, "slot-1", got.GetSlotId())
	require.Equal(t, "attempt-1", got.GetAttemptId())
	require.Equal(t, int64(11), got.GetSequenceNumber())
	require.Equal(t, int64(12345), got.GetTimestampMs())
}

func TestPlanMediaProcessingLifecycleEventBuildsReadyAudioEvent(t *testing.T) {
	duration := 157
	snapshot := mediaProcessingSnapshot{
		File: model.File{
			ID:              "file-1",
			MimeType:        "audio/ogg",
			DurationSeconds: &duration,
		},
		Derivatives: map[string]mediaProcessingDerivative{
			managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String(): {
				MediaGenerationID: optionalString("hls-generation"),
			},
			managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_SPECTROGRAM.String(): {
				AssetID: optionalString("spectrogram-asset"),
			},
			managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM.String(): {
				AssetID: optionalString("waveform-asset"),
			},
		},
	}
	handlers := &Handlers{}

	got, err := handlers.planMediaProcessingLifecycleEvent(
		mediaProcessingPublishOptions{
			EntityType:    managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK,
			EntityID:      "track-1",
			FileID:        "file-1",
			CorrelationID: "correlation-ready",
			Sequence:      12,
			TimestampMs:   12346,
		},
		mediaProcessingLifecycleIdentity{
			TrackID:   "track-1",
			ReleaseID: "release-1",
		},
		snapshot,
	)

	require.NoError(t, err)
	require.Equal(t, "correlation-ready", got.GetCorrelationId())
	require.Equal(t, commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_READY, got.GetStatus())
	require.Nil(t, got.Percentage)
	require.Nil(t, got.Error)
	require.NotNil(t, got.Outputs)
	require.Equal(t, int32(duration), got.Outputs.GetDurationSeconds())
	require.NotEmpty(t, got.Outputs.GetHlsGenerationId())
	require.NotEmpty(t, got.Outputs.GetSpectrogramAssetId())
	require.NotEmpty(t, got.Outputs.GetWaveformAssetId())
	require.Equal(t, int64(12), got.GetSequenceNumber())
	require.Equal(t, int64(12346), got.GetTimestampMs())
}

func TestPlanMediaProcessingLifecycleEventBuildsFailedEventFromError(t *testing.T) {
	snapshot := mediaProcessingSnapshot{
		File: model.File{
			ID:       "file-1",
			MimeType: "audio/ogg",
		},
		Derivatives: map[string]mediaProcessingDerivative{},
	}

	got, err := (&Handlers{}).planMediaProcessingLifecycleEvent(
		mediaProcessingPublishOptions{
			EntityType:    managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK,
			EntityID:      "track-1",
			FileID:        "file-1",
			CorrelationID: "correlation-failed",
			TimestampMs:   12347,
			Error:         "transcode failed",
		},
		mediaProcessingLifecycleIdentity{
			TrackID:   "track-1",
			ReleaseID: "release-1",
		},
		snapshot,
	)

	require.NoError(t, err)
	require.Equal(t, "correlation-failed", got.GetCorrelationId())
	require.Equal(t, commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_FAILED, got.GetStatus())
	require.Nil(t, got.Percentage)
	require.Nil(t, got.Outputs)
	require.Equal(t, "transcode failed", got.GetError())
	require.Equal(t, int64(12347), got.GetTimestampMs())
}

func TestBuildMediaProcessingLifecycleOutputsRequiresReadyAudioContract(t *testing.T) {
	duration := 157
	snapshot := mediaProcessingSnapshot{
		File: model.File{
			ID:              randomWorkerTranscodeUUID(),
			MimeType:        "audio/ogg",
			DurationSeconds: &duration,
		},
		Derivatives: map[string]mediaProcessingDerivative{
			managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String(): {
				MediaGenerationID: optionalString("hls-generation"),
			},
			managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_SPECTROGRAM.String(): {
				AssetID: optionalString("spectrogram-asset"),
			},
			managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM.String(): {
				AssetID: optionalString("waveform-asset"),
			},
		},
	}
	handlers := &Handlers{}

	outputs, err := handlers.buildMediaProcessingLifecycleOutputs(snapshot)

	require.NoError(t, err)
	require.NotNil(t, outputs)
	require.NotNil(t, outputs.HlsGenerationId)
	require.NotNil(t, outputs.SpectrogramAssetId)
	require.NotNil(t, outputs.WaveformAssetId)
	require.NotNil(t, outputs.DurationSeconds)
	require.Equal(t, int32(duration), *outputs.DurationSeconds)
}

func TestBuildMediaProcessingLifecycleOutputsFailsMissingReadyDuration(t *testing.T) {
	snapshot := mediaProcessingSnapshot{
		File: model.File{
			ID:       randomWorkerTranscodeUUID(),
			MimeType: "audio/ogg",
		},
		Derivatives: map[string]mediaProcessingDerivative{
			managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String(): {
				MediaGenerationID: optionalString("hls-generation"),
			},
			managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_SPECTROGRAM.String(): {
				AssetID: optionalString("spectrogram-asset"),
			},
			managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM.String(): {
				AssetID: optionalString("waveform-asset"),
			},
		},
	}
	handlers := &Handlers{}

	_, err := handlers.buildMediaProcessingLifecycleOutputs(snapshot)

	require.Error(t, err)
	require.Contains(t, err.Error(), "duration_seconds")
}
