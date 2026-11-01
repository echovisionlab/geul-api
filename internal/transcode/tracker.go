package transcode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

// Tracker persists queryable transcode domain progress. PGMQ owns command
// delivery retry and DLQ state; this tracker never scans or republishes jobs.
type Tracker struct {
	units     mutation.UnitOfWork[Store]
	publisher mq.TranscoderPublisher
}

type transcodeCancelPublisher interface {
	PublishTranscodeCancel(ctx context.Context, event *managev1.TranscodeCancelEvent) error
}

func NewTracker(units mutation.UnitOfWork[Store], publisher mq.TranscoderPublisher) *Tracker {
	if units == nil {
		panic("transcode.NewTracker: units are required")
	}
	if publisher == nil {
		panic("transcode.NewTracker: publisher is required")
	}
	return &Tracker{units: units, publisher: publisher}
}

// PublishTranscodeAudio implements mq.TranscoderPublisher with DB tracking.
func (t *Tracker) PublishTranscodeAudio(ctx context.Context, job *managev1.TranscodeAudioEvent) error {
	return t.publishTrackedJob(ctx, func(store Store) error {
		return RegisterAudio(ctx, store, job)
	}, func(ctx context.Context) error {
		return t.publisher.PublishTranscodeAudio(ctx, job)
	})
}

// PublishTranscodeVideo implements mq.TranscoderPublisher with DB tracking.
func (t *Tracker) PublishTranscodeVideo(ctx context.Context, job *managev1.TranscodeVideoEvent) error {
	return t.publishTrackedJob(ctx, func(store Store) error {
		return RegisterVideo(ctx, store, job)
	}, func(ctx context.Context) error {
		return t.publisher.PublishTranscodeVideo(ctx, job)
	})
}

// PublishWaveformCancel forwards waveform cancel events through the shared publisher.
func (t *Tracker) PublishWaveformCancel(ctx context.Context, event *managev1.WaveformCancelEvent) error {
	return t.publisher.PublishWaveformCancel(ctx, event)
}

// PublishTranscodeCancel forwards transcode cancel events when the underlying publisher supports them.
func (t *Tracker) PublishTranscodeCancel(ctx context.Context, event *managev1.TranscodeCancelEvent) error {
	if publisher, ok := t.publisher.(transcodeCancelPublisher); ok {
		return publisher.PublishTranscodeCancel(ctx, event)
	}
	return nil
}

func (t *Tracker) publishTrackedJob(
	ctx context.Context,
	register func(Store) error,
	publish func(context.Context) error,
) error {
	if err := t.units.Transaction(ctx, false, func(tx mutation.Tx[Store]) error {
		return register(tx.Store())
	}); err != nil {
		return err
	}

	// A publisher-confirm error is deliberately not mirrored into PostgreSQL.
	// The synchronous caller retries this same immutable message ID; duplicate
	// broker delivery is handled by the consumer's idempotent output contract.
	return publish(ctx)
}

// RegisterAudio stores the immutable audio allocation in store's
// transaction. The caller must enqueue the same command through that
// transaction before committing it.
func RegisterAudio(ctx context.Context, store Store, job *managev1.TranscodeAudioEvent) error {
	if job == nil {
		return errors.New("audio transcode job is nil")
	}
	if err := ValidateAudioTranscodeEvent(job); err != nil {
		return err
	}
	return registerTrackedJob(ctx, store, eventpkg.QueueTranscoderAudio,
		job.EventId, job.EntityType.String(), job.EntityId, job.FileId, job)
}

// RegisterVideo stores the immutable video allocation in store's
// transaction. The caller must enqueue the same command through that
// transaction before committing it.
func RegisterVideo(ctx context.Context, store Store, job *managev1.TranscodeVideoEvent) error {
	if job == nil {
		return errors.New("video transcode job is nil")
	}
	if err := ValidateVideoTranscodeEvent(job); err != nil {
		return err
	}
	return registerTrackedJob(ctx, store, eventpkg.QueueTranscoderVideo,
		job.EventId, job.EntityType.String(), job.EntityId, job.FileId, job)
}

func registerTrackedJob(
	ctx context.Context,
	store Store,
	queueName, eventID, entityType, entityID, fileID string,
	msg proto.Message,
) error {
	payload, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal transcode payload: %w", err)
	}

	now := time.Now()
	inserted, err := store.InsertJob(ctx, Job{
		EventID: eventID, QueueName: queueName, EntityType: entityType, EntityID: entityID, FileID: fileID,
		Payload: payload, Status: StatusQueued, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return fmt.Errorf("save transcode job %s: %w", eventID, err)
	}
	if inserted {
		if err := store.CancelOtherActiveJobs(ctx, eventID, queueName, fileID, "superseded by newer transcode request", now); err != nil {
			return fmt.Errorf("supersede previous transcode jobs: %w", err)
		}
		return nil
	}

	existing, err := store.LockJob(ctx, eventID)
	if err != nil {
		return fmt.Errorf("load transcode job %s: %w", eventID, err)
	}
	if existing.QueueName != queueName || existing.EntityType != entityType ||
		existing.EntityID != entityID || existing.FileID != fileID ||
		!bytes.Equal(existing.Payload, payload) {
		return fmt.Errorf("transcode job %s conflicts with existing immutable payload", eventID)
	}
	return nil
}

// HandleTranscodeProgress updates queryable progress for tracked jobs.
func (t *Tracker) HandleTranscodeProgress(ctx context.Context, event *managev1.TranscodeProgressEvent) error {
	if event == nil || strings.TrimSpace(event.EventId) == "" {
		return nil
	}

	return t.units.Transaction(ctx, false, func(tx mutation.Tx[Store]) error {
		store := tx.Store()
		job, found, err := store.LockUnsettledJob(ctx, event.EventId)
		if err != nil || !found {
			return err
		}
		return store.RecordProgress(ctx, event.EventId, transcodeProgress(job, event), time.Now())
	})
}

func transcodeProgress(job Job, event *managev1.TranscodeProgressEvent) Progress {
	lastSequence := job.LastSequence
	isFreshSequence := event.SequenceNumber <= 0 ||
		lastSequence == nil ||
		event.SequenceNumber > *lastSequence
	progress := clampTranscodeProgress(event.Progress)
	hlsProgress := job.HLSProgress
	spectrogramProgress := job.SpectrogramProgress
	if isFreshSequence {
		hlsProgress, spectrogramProgress = applyTranscodeComponentProgress(
			job.QueueName,
			event.GetStage(),
			progress,
			hlsProgress,
			spectrogramProgress,
		)
	}

	result := Progress{
		Progress:            max(job.Progress, progress),
		HLSProgress:         hlsProgress,
		SpectrogramProgress: spectrogramProgress,
	}
	if isFreshSequence && event.SequenceNumber > 0 {
		result.LastSequence = &event.SequenceNumber
	}
	if isFreshSequence && event.Stage != nil {
		stage := event.GetStage().String()
		result.LastStage = &stage
	}
	return result
}

func clampTranscodeProgress(progress int32) int {
	if progress < 0 {
		return 0
	}
	if progress > 100 {
		return 100
	}
	return int(progress)
}

func applyTranscodeComponentProgress(
	queueName string,
	stage managev1.TranscodeStage,
	progress int,
	currentHLSProgress int,
	currentSpectrogramProgress int,
) (int, int) {
	hlsProgress := currentHLSProgress
	spectrogramProgress := currentSpectrogramProgress

	if queueName == eventpkg.QueueTranscoderVideo {
		switch stage {
		case managev1.TranscodeStage_TRANSCODE_STAGE_DOWNLOADING:
			hlsProgress = max(hlsProgress, stageProgress(progress, 0, 10))
		case managev1.TranscodeStage_TRANSCODE_STAGE_PROCESSING,
			managev1.TranscodeStage_TRANSCODE_STAGE_HLS_PROCESSING:
			hlsProgress = max(hlsProgress, stageProgress(progress, 10, 90))
		case managev1.TranscodeStage_TRANSCODE_STAGE_UPLOADING,
			managev1.TranscodeStage_TRANSCODE_STAGE_HLS_UPLOADING:
			hlsProgress = max(hlsProgress, stageProgress(progress, 90, 100))
		}
		return hlsProgress, spectrogramProgress
	}

	switch stage {
	case managev1.TranscodeStage_TRANSCODE_STAGE_DOWNLOADING:
		sharedProgress := stageProgress(progress, 0, 10)
		hlsProgress = max(hlsProgress, sharedProgress)
		spectrogramProgress = max(spectrogramProgress, sharedProgress)
	case managev1.TranscodeStage_TRANSCODE_STAGE_SPECTROGRAM_PROCESSING:
		hlsProgress = max(hlsProgress, 10)
		spectrogramProgress = max(spectrogramProgress, stageProgress(progress, 10, 85))
	case managev1.TranscodeStage_TRANSCODE_STAGE_SPECTROGRAM_UPLOADING:
		hlsProgress = max(hlsProgress, 10)
		spectrogramProgress = max(spectrogramProgress, stageProgress(progress, 85, 100))
	case managev1.TranscodeStage_TRANSCODE_STAGE_PROCESSING,
		managev1.TranscodeStage_TRANSCODE_STAGE_HLS_PROCESSING:
		hlsProgress = max(hlsProgress, stageProgress(progress, 10, 90))
		spectrogramProgress = max(spectrogramProgress, 100)
	case managev1.TranscodeStage_TRANSCODE_STAGE_UPLOADING,
		managev1.TranscodeStage_TRANSCODE_STAGE_HLS_UPLOADING:
		hlsProgress = max(hlsProgress, stageProgress(progress, 90, 100))
		spectrogramProgress = max(spectrogramProgress, 100)
	}

	return hlsProgress, spectrogramProgress
}

func stageProgress(progress int, start int, end int) int {
	progress = min(max(progress, 0), 100)
	return start + (((end-start)*progress + 50) / 100)
}

// HandleTranscodeComplete marks tracked job terminal state.
func (t *Tracker) HandleTranscodeComplete(ctx context.Context, event *managev1.TranscodeCompleteEvent) error {
	if event == nil || strings.TrimSpace(event.FileId) == "" {
		return nil
	}
	if strings.TrimSpace(event.EventId) == "" {
		return fmt.Errorf("transcode completion event_id is required")
	}

	queueName := queueNameFromCompleteType(event.EventType)
	if queueName == "" {
		return nil
	}

	now := time.Now()
	jobs, err := t.completionCandidates(ctx, event, queueName)
	if err != nil {
		return err
	}
	if event.Success {
		job, err := matchingCompletionJob(event, jobs)
		if err != nil || job == nil {
			return err
		}
		return t.units.Transaction(ctx, false, func(tx mutation.Tx[Store]) error {
			return tx.Store().DeleteJob(ctx, job.EventID)
		})
	}

	if len(jobs) != 1 || !slices.Contains(ActiveStatuses(), jobs[0].Status) {
		slog.WarnContext(ctx, "Skipped uncorrelated transcode failure completion",
			failurecause.Attr(errors.New(event.GetError())),
			"entityType", event.EntityType.String(),
			"entityId", event.EntityId,
			"fileId", event.FileId,
			"candidateJobs", len(jobs),
		)
		return nil
	}
	errText := "transcode failed"
	if event.Error != nil && strings.TrimSpace(*event.Error) != "" {
		errText = *event.Error
	}
	return t.units.Transaction(ctx, false, func(tx mutation.Tx[Store]) error {
		return tx.Store().FailJob(ctx, jobs[0].EventID, errText, now)
	})
}

func (t *Tracker) completionCandidates(
	ctx context.Context,
	event *managev1.TranscodeCompleteEvent,
	queueName string,
) ([]Job, error) {
	var jobs []Job
	err := t.units.Read(ctx, func(store Store) (err error) {
		jobs, err = store.CompletionCandidates(ctx, CompletionIdentity{
			EventID: event.EventId, QueueName: queueName, EntityType: event.EntityType.String(),
			EntityID: event.EntityId, FileID: event.FileId,
		})
		return err
	})
	return jobs, err
}

func matchingCompletionJob(event *managev1.TranscodeCompleteEvent, jobs []Job) (*Job, error) {
	for i := range jobs {
		matches, err := MatchTranscodeCompletionPayload(event, jobs[i].Payload)
		if err != nil {
			return nil, err
		}
		if matches {
			return &jobs[i], nil
		}
	}
	return nil, nil
}

// MarkCancelled marks active tracked jobs for a file as cancelled.
func (t *Tracker) MarkCancelled(ctx context.Context, fileID string, reason managev1.TranscodeCancelReason) error {
	if strings.TrimSpace(fileID) == "" {
		return nil
	}
	return t.units.Transaction(ctx, false, func(tx mutation.Tx[Store]) error {
		return tx.Store().CancelFileJobs(ctx, fileID, CancelReason(reason), time.Now())
	})
}

// CancelReason is the last error a job cancelled for reason records.
func CancelReason(reason managev1.TranscodeCancelReason) string {
	return fmt.Sprintf("cancelled: %s", reason.String())
}

func queueNameFromCompleteType(eventType managev1.TranscodeEventType) string {
	switch eventType {
	case managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO:
		return eventpkg.QueueTranscoderAudio
	case managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_VIDEO:
		return eventpkg.QueueTranscoderVideo
	default:
		return ""
	}
}

// WaveformCancelPublisher publishes the cancellation of a waveform job.
type WaveformCancelPublisher interface {
	PublishWaveformCancel(ctx context.Context, event *managev1.WaveformCancelEvent) error
}

// CancelTrackWaveformJobs marks active Track waveform jobs cancelled in
// store's transaction before publishing their cancellation commands.
func CancelTrackWaveformJobs(
	ctx context.Context,
	store Store,
	publisher WaveformCancelPublisher,
	trackIDs []string,
	reason managev1.TranscodeCancelReason,
) error {
	if len(trackIDs) == 0 {
		return nil
	}
	jobs, err := store.QueuedTrackWaveformJobs(ctx, trackIDs)
	if err != nil || len(jobs) == 0 {
		return err
	}
	jobEventIDs := make([]string, 0, len(jobs))
	for _, job := range jobs {
		jobEventIDs = append(jobEventIDs, job.EventID)
	}
	if err := store.CancelWaveformJobs(ctx, jobEventIDs, CancelReason(reason), time.Now()); err != nil {
		return err
	}
	if publisher == nil {
		return nil
	}
	for _, job := range jobs {
		if err := publisher.PublishWaveformCancel(ctx, &managev1.WaveformCancelEvent{
			EventId:     job.EventID,
			EntityType:  managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK,
			EntityId:    job.EntityID,
			FileId:      job.FileID,
			Reason:      reason,
			TimestampMs: time.Now().UnixMilli(),
		}); err != nil {
			return err
		}
	}
	return nil
}
