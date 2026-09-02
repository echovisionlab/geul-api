package transcode

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/stretchr/testify/require"
)

func TestTranscodeJobModelOmitsBrokerTransportState(t *testing.T) {
	t.Parallel()

	modelType := reflect.TypeFor[model.TranscodeJob]()
	for _, fieldName := range []string{"Attempts", "LastHeartbeat"} {
		_, found := modelType.FieldByName(fieldName)
		require.False(t, found, "transcode_job must not mirror queue transport field %s", fieldName)
	}
}

func TestApplyTranscodeComponentProgressAggregatesAudioHLSAfterSpectrogram(t *testing.T) {
	hlsProgress, spectrogramProgress := applyTranscodeComponentProgress(
		eventpkg.QueueTranscoderAudio,
		managev1.TranscodeStage_TRANSCODE_STAGE_HLS_PROCESSING,
		64,
		0,
		0,
	)

	require.Equal(t, 61, hlsProgress)
	require.Equal(t, 100, spectrogramProgress)
	require.Equal(t, int32(54), int32((hlsProgress+spectrogramProgress+1)/3))
}

func TestApplyTranscodeComponentProgressDoesNotRegressVideoDuringThumbnailStage(t *testing.T) {
	hlsProgress, spectrogramProgress := applyTranscodeComponentProgress(
		eventpkg.QueueTranscoderVideo,
		managev1.TranscodeStage_TRANSCODE_STAGE_THUMBNAIL_PROCESSING,
		100,
		10,
		0,
	)

	require.Equal(t, 10, hlsProgress)
	require.Equal(t, 0, spectrogramProgress)
}

func TestApplyTranscodeComponentProgressRoundsStageProgress(t *testing.T) {
	hlsProgress, _ := applyTranscodeComponentProgress(
		eventpkg.QueueTranscoderVideo,
		managev1.TranscodeStage_TRANSCODE_STAGE_HLS_PROCESSING,
		63,
		0,
		0,
	)

	require.Equal(t, 60, hlsProgress)
}

func TestTranscodeProgressUpdatesApplyFreshSequenceOnlyToComponentState(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()
	initialSequence := int64(5)
	initialStage := managev1.TranscodeStage_TRANSCODE_STAGE_DOWNLOADING.String()
	job := model.TranscodeJob{
		QueueName:           eventpkg.QueueTranscoderAudio,
		Status:              StatusQueued,
		Progress:            20,
		HLSProgress:         20,
		SpectrogramProgress: 20,
		LastSequence:        &initialSequence,
		LastStage:           &initialStage,
	}

	staleStage := managev1.TranscodeStage_TRANSCODE_STAGE_HLS_UPLOADING
	staleUpdates := transcodeProgressUpdates(job, &managev1.TranscodeProgressEvent{
		SequenceNumber: 4,
		Progress:       99,
		Stage:          &staleStage,
	}, now)

	require.Equal(t, StatusProcessing, staleUpdates["status"])
	require.Equal(t, 99, staleUpdates["progress"])
	require.Equal(t, 20, staleUpdates["hls_progress"])
	require.Equal(t, 20, staleUpdates["spectrogram_progress"])
	require.NotContains(t, staleUpdates, "last_heartbeat")
	require.Equal(t, now, staleUpdates["updated_at"])
	require.NotContains(t, staleUpdates, "last_sequence")
	require.NotContains(t, staleUpdates, "last_stage")

	job.Progress = 99
	freshStage := managev1.TranscodeStage_TRANSCODE_STAGE_SPECTROGRAM_PROCESSING
	freshUpdates := transcodeProgressUpdates(job, &managev1.TranscodeProgressEvent{
		SequenceNumber: 6,
		Progress:       50,
		Stage:          &freshStage,
	}, now)

	require.Equal(t, 99, freshUpdates["progress"])
	require.Equal(t, 20, freshUpdates["hls_progress"])
	require.Equal(t, 48, freshUpdates["spectrogram_progress"])
	require.Equal(t, int64(6), freshUpdates["last_sequence"])
	require.Equal(t, freshStage.String(), freshUpdates["last_stage"])
}

func TestTranscodeCancelledUpdatesMarkTerminalState(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0).UTC()

	require.Equal(t, structured.Fields{
		"status":       StatusCancelled,
		"last_error":   "cancelled: TRANSCODE_CANCEL_REASON_USER_CANCELLED",
		"updated_at":   now,
		"completed_at": now,
	}, transcodeCancelledUpdates(managev1.TranscodeCancelReason_TRANSCODE_CANCEL_REASON_USER_CANCELLED, now))
}

func TestTrackerCancelForwardingUsesUnderlyingPublisher(t *testing.T) {
	t.Parallel()

	publisher := &trackerUnitPublisher{}
	tracker := &Tracker{publisher: publisher}

	waveformCancel := &managev1.WaveformCancelEvent{
		EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		EntityId:   "post-1",
		FileId:     "file-1",
		Reason:     managev1.TranscodeCancelReason_TRANSCODE_CANCEL_REASON_USER_CANCELLED,
	}
	transcodeCancel := &managev1.TranscodeCancelEvent{
		EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		EntityId:   "post-1",
		FileId:     "file-1",
		Reason:     managev1.TranscodeCancelReason_TRANSCODE_CANCEL_REASON_USER_CANCELLED,
	}

	require.NoError(t, tracker.PublishWaveformCancel(context.Background(), waveformCancel))
	require.NoError(t, tracker.PublishTranscodeCancel(context.Background(), transcodeCancel))
	require.Equal(t, []*managev1.WaveformCancelEvent{waveformCancel}, publisher.waveformCancelEvents)
	require.Equal(t, []*managev1.TranscodeCancelEvent{transcodeCancel}, publisher.transcodeCancelEvents)
}

type trackerUnitPublisher struct {
	waveformCancelEvents  []*managev1.WaveformCancelEvent
	transcodeCancelEvents []*managev1.TranscodeCancelEvent
}

func (p *trackerUnitPublisher) PublishTranscodeAudio(context.Context, *managev1.TranscodeAudioEvent) error {
	return nil
}

func (p *trackerUnitPublisher) PublishTranscodeVideo(context.Context, *managev1.TranscodeVideoEvent) error {
	return nil
}

func (p *trackerUnitPublisher) PublishWaveformCancel(_ context.Context, event *managev1.WaveformCancelEvent) error {
	p.waveformCancelEvents = append(p.waveformCancelEvents, event)
	return nil
}

func (p *trackerUnitPublisher) PublishTranscodeCancel(_ context.Context, event *managev1.TranscodeCancelEvent) error {
	p.transcodeCancelEvents = append(p.transcodeCancelEvents, event)
	return nil
}
