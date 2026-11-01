package transcode_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/transcode"
	"github.com/echovisionlab/geul-api/internal/transcode/transcodetest"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

func TestPublishTranscodeAudioTracksTheJobAndSupersedesThePreviousOne(t *testing.T) {
	store, units := transcodetest.New()
	fileID := uuid.NewString()
	previous := transcodetest.QueuedJob(fileID, time.Now())
	previous.Status = transcode.StatusProcessing
	store.SeedJob(previous)
	publisher := &recordingPublisher{}
	tracker := transcode.NewTracker(units, publisher)
	job := transcodetest.AudioJob(t, fileID)

	require.NoError(t, tracker.PublishTranscodeAudio(t.Context(), job))
	require.Len(t, publisher.audioEvents, 1)
	superseded, _ := store.Job(previous.EventID)
	require.Equal(t, transcode.StatusCancelled, superseded.Status)
	require.Contains(t, *superseded.LastError, "superseded")
	require.NotNil(t, superseded.CompletedAt)
	current, ok := store.Job(job.EventId)
	require.True(t, ok)
	require.Equal(t, eventpkg.QueueTranscoderAudio, current.QueueName)
	require.Equal(t, transcode.StatusQueued, current.Status)
	require.Equal(t, "TRANSCODE_ENTITY_TYPE_POST", current.EntityType)
	var saved managev1.TranscodeAudioEvent
	require.NoError(t, proto.Unmarshal(current.Payload, &saved))
	require.True(t, proto.Equal(job, &saved))
}

func TestPublishTranscodeIsIdempotentForAStableCommand(t *testing.T) {
	store, units := transcodetest.New()
	publisher := &recordingPublisher{}
	tracker := transcode.NewTracker(units, publisher)
	job := transcodetest.AudioJob(t, uuid.NewString())

	require.NoError(t, tracker.PublishTranscodeAudio(t.Context(), job))
	require.NoError(t, tracker.PublishTranscodeAudio(t.Context(), proto.Clone(job).(*managev1.TranscodeAudioEvent)))
	require.Len(t, publisher.audioEvents, 2)
	saved, _ := store.Job(job.EventId)
	require.Equal(t, transcode.StatusQueued, saved.Status)

	conflict := proto.Clone(job).(*managev1.TranscodeAudioEvent)
	conflict.EntityId = uuid.NewString()
	require.ErrorContains(t, tracker.PublishTranscodeAudio(t.Context(), conflict), "conflicts with existing immutable payload")
	require.Len(t, publisher.audioEvents, 2)
}

func TestPublishTranscodeKeepsTheJobQueuedWhenPublishingFails(t *testing.T) {
	store, units := transcodetest.New()
	publisher := &recordingPublisher{audioFailuresRemaining: 1}
	tracker := transcode.NewTracker(units, publisher)
	job := transcodetest.AudioJob(t, uuid.NewString())

	require.ErrorContains(t, tracker.PublishTranscodeAudio(t.Context(), job), "injected audio publish failure")
	queued, _ := store.Job(job.EventId)
	require.Equal(t, transcode.StatusQueued, queued.Status)
	require.Nil(t, queued.LastError)
	require.NoError(t, tracker.PublishTranscodeAudio(t.Context(), proto.Clone(job).(*managev1.TranscodeAudioEvent)))
	require.Len(t, publisher.audioEvents, 1)
}

func TestPublishTranscodeRejectsAnInvalidCommandBeforeStoringIt(t *testing.T) {
	store, units := transcodetest.New()
	tracker := transcode.NewTracker(units, &recordingPublisher{})
	video := transcodetest.VideoJob(t, uuid.NewString())
	video.ThumbnailOutput.Extension = "png"

	require.Error(t, tracker.PublishTranscodeVideo(t.Context(), video))
	_, stored := store.Job(video.EventId)
	require.False(t, stored)
	require.NoError(t, tracker.PublishTranscodeVideo(t.Context(), transcodetest.VideoJob(t, uuid.NewString())))
	require.Error(t, units.Transaction(t.Context(), false, func(tx mutation.Tx[transcode.Store]) error {
		return transcode.RegisterAudio(t.Context(), tx.Store(), nil)
	}))
}

func TestHandleTranscodeProgressRecordsOnlyUnsettledJobs(t *testing.T) {
	store, units := transcodetest.New()
	tracker := transcode.NewTracker(units, &recordingPublisher{})
	job := transcodetest.QueuedJob(uuid.NewString(), time.Now())
	cancelled := transcodetest.QueuedJob(uuid.NewString(), time.Now())
	cancelled.Status = transcode.StatusCancelled
	store.SeedJob(job)
	store.SeedJob(cancelled)
	stage := managev1.TranscodeStage_TRANSCODE_STAGE_DOWNLOADING

	for _, eventID := range []string{job.EventID, cancelled.EventID, uuid.NewString(), " "} {
		require.NoError(t, tracker.HandleTranscodeProgress(t.Context(), &managev1.TranscodeProgressEvent{
			EventId: eventID, Progress: 140, SequenceNumber: 3, Stage: &stage,
		}))
	}
	progressed, _ := store.Job(job.EventID)
	require.Equal(t, transcode.StatusProcessing, progressed.Status)
	require.Equal(t, 100, progressed.Progress)
	require.Equal(t, 10, progressed.HLSProgress)
	require.Equal(t, int64(3), *progressed.LastSequence)
	untouched, _ := store.Job(cancelled.EventID)
	require.Equal(t, transcode.StatusCancelled, untouched.Status)
	require.NoError(t, tracker.HandleTranscodeProgress(t.Context(), nil))
}

func TestHandleTranscodeCompleteSettlesOnlyTheCorrelatedJob(t *testing.T) {
	store, units := transcodetest.New()
	tracker := transcode.NewTracker(units, &recordingPublisher{})
	fileID := uuid.NewString()
	job := transcodetest.AudioJob(t, fileID)
	require.NoError(t, tracker.PublishTranscodeAudio(t.Context(), job))

	failure := "late failure from an uncorrelated attempt"
	require.NoError(t, tracker.HandleTranscodeComplete(t.Context(), &managev1.TranscodeCompleteEvent{
		EventId: uuid.NewString(), EventType: managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO,
		EntityType: job.EntityType, EntityId: job.EntityId, FileId: fileID, Error: &failure,
	}))
	stored, _ := store.Job(job.EventId)
	require.Equal(t, transcode.StatusQueued, stored.Status)

	mismatched := &managev1.TranscodeCompleteEvent{
		EventId: job.EventId, EventType: managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO,
		EntityType: job.EntityType, EntityId: job.EntityId, FileId: fileID, Success: true,
		Outputs: &managev1.TranscodeOutputs{
			Hls:         &commonv1.MediaGenerationWriteResult{GenerationId: uuid.NewString()},
			Spectrogram: &commonv1.AssetWriteResult{AssetId: job.GetSpectrogramOutput().GetAssetId()},
		},
	}
	require.NoError(t, tracker.HandleTranscodeComplete(t.Context(), mismatched))
	_, kept := store.Job(job.EventId)
	require.True(t, kept, "a completion of other outputs leaves the job")

	mismatched.Outputs.Hls.GenerationId = job.GetHlsOutput().GetGenerationId()
	require.NoError(t, tracker.HandleTranscodeComplete(t.Context(), mismatched))
	_, kept = store.Job(job.EventId)
	require.False(t, kept)
	require.Error(t, tracker.HandleTranscodeComplete(t.Context(), &managev1.TranscodeCompleteEvent{FileId: fileID}))
}

func TestHandleTranscodeCompleteFailsTheSingleActiveJob(t *testing.T) {
	store, units := transcodetest.New()
	tracker := transcode.NewTracker(units, &recordingPublisher{})
	job := transcodetest.VideoJob(t, uuid.NewString())
	require.NoError(t, tracker.PublishTranscodeVideo(t.Context(), job))

	require.NoError(t, tracker.HandleTranscodeComplete(t.Context(), &managev1.TranscodeCompleteEvent{
		EventId: job.EventId, EventType: managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_VIDEO,
		EntityType: job.EntityType, EntityId: job.EntityId, FileId: job.FileId,
	}))
	failed, _ := store.Job(job.EventId)
	require.Equal(t, transcode.StatusFailedTerminal, failed.Status)
	require.Equal(t, "transcode failed", *failed.LastError)
}

func TestMarkCancelledCancelsTheActiveJobsOfAFile(t *testing.T) {
	store, units := transcodetest.New()
	job := transcodetest.QueuedJob(uuid.NewString(), time.Now())
	store.SeedJob(job)
	tracker := transcode.NewTracker(units, &recordingPublisher{})

	require.NoError(t, tracker.MarkCancelled(t.Context(), " ", managev1.TranscodeCancelReason_TRANSCODE_CANCEL_REASON_USER_DELETED))
	require.NoError(t, tracker.MarkCancelled(t.Context(), job.FileID, managev1.TranscodeCancelReason_TRANSCODE_CANCEL_REASON_USER_DELETED))
	cancelled, _ := store.Job(job.EventID)
	require.Equal(t, transcode.StatusCancelled, cancelled.Status)
	require.Equal(t, "cancelled: TRANSCODE_CANCEL_REASON_USER_DELETED", *cancelled.LastError)
}

func TestCancelTrackWaveformJobsCancelsBeforePublishing(t *testing.T) {
	store, units := transcodetest.New()
	trackID := uuid.NewString()
	job := transcode.WaveformJob{
		EventID: uuid.NewString(), EntityType: "TRANSCODE_ENTITY_TYPE_TRACK", EntityID: trackID,
		FileID: uuid.NewString(), Status: transcode.WaveformJobStatusQueued,
	}
	store.SeedWaveformJob(job)
	publisher := &recordingPublisher{}
	reason := managev1.TranscodeCancelReason_TRANSCODE_CANCEL_REASON_USER_DELETED

	require.NoError(t, units.Transaction(t.Context(), false, func(tx mutation.Tx[transcode.Store]) error {
		return transcode.CancelTrackWaveformJobs(t.Context(), tx.Store(), publisher, []string{trackID}, reason)
	}))
	cancelled, _ := store.WaveformJob(job.EventID)
	require.Equal(t, transcode.WaveformJobStatusCancelled, cancelled.Status)
	require.True(t, cancelled.CancelRequested)
	require.Len(t, publisher.waveformCancelEvents, 1)
	require.Equal(t, job.FileID, publisher.waveformCancelEvents[0].GetFileId())
	require.Equal(t, reason, publisher.waveformCancelEvents[0].GetReason())

	publisher.err = errors.New("broker down")
	store.SeedWaveformJob(job)
	err := units.Transaction(t.Context(), false, func(tx mutation.Tx[transcode.Store]) error {
		return transcode.CancelTrackWaveformJobs(t.Context(), tx.Store(), publisher, []string{trackID}, reason)
	})
	require.ErrorIs(t, err, publisher.err)
	rolledBack, _ := store.WaveformJob(job.EventID)
	require.Equal(t, transcode.WaveformJobStatusQueued, rolledBack.Status)
}

type recordingPublisher struct {
	audioEvents            []*managev1.TranscodeAudioEvent
	audioFailuresRemaining int
	videoEvents            []*managev1.TranscodeVideoEvent
	waveformCancelEvents   []*managev1.WaveformCancelEvent
	transcodeCancelEvents  []*managev1.TranscodeCancelEvent
	err                    error
}

func (p *recordingPublisher) PublishTranscodeAudio(_ context.Context, event *managev1.TranscodeAudioEvent) error {
	if p.audioFailuresRemaining > 0 {
		p.audioFailuresRemaining--
		return fmt.Errorf("injected audio publish failure")
	}
	p.audioEvents = append(p.audioEvents, event)
	return nil
}

func (p *recordingPublisher) PublishTranscodeVideo(_ context.Context, event *managev1.TranscodeVideoEvent) error {
	p.videoEvents = append(p.videoEvents, event)
	return nil
}

func (p *recordingPublisher) PublishWaveformCancel(_ context.Context, event *managev1.WaveformCancelEvent) error {
	if p.err != nil {
		return p.err
	}
	p.waveformCancelEvents = append(p.waveformCancelEvents, event)
	return nil
}

func (p *recordingPublisher) PublishTranscodeCancel(_ context.Context, event *managev1.TranscodeCancelEvent) error {
	p.transcodeCancelEvents = append(p.transcodeCancelEvents, event)
	return nil
}
