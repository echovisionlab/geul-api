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
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

const (
	StatusQueued         = "TRANSCODE_JOB_STATUS_QUEUED"
	StatusProcessing     = "TRANSCODE_JOB_STATUS_PROCESSING"
	StatusCompleted      = "TRANSCODE_JOB_STATUS_COMPLETED"
	StatusFailedTerminal = "TRANSCODE_JOB_STATUS_FAILED_TERMINAL"
	StatusCancelled      = "TRANSCODE_JOB_STATUS_CANCELLED"
)

// Tracker persists queryable transcode domain progress. PGMQ owns command
// delivery retry and DLQ state; this tracker never scans or republishes jobs.
type Tracker struct {
	db        *gorm.DB
	publisher mq.TranscoderPublisher
}

type transcodeCancelPublisher interface {
	PublishTranscodeCancel(ctx context.Context, event *managev1.TranscodeCancelEvent) error
}

func NewTracker(db *gorm.DB, publisher mq.TranscoderPublisher) *Tracker {
	if db == nil {
		panic("transcode.NewTracker: db is required")
	}
	if publisher == nil {
		panic("transcode.NewTracker: publisher is required")
	}

	return &Tracker{
		db:        db,
		publisher: publisher,
	}
}

// PublishTranscodeAudio implements mq.TranscoderPublisher with DB tracking.
func (t *Tracker) PublishTranscodeAudio(ctx context.Context, job *managev1.TranscodeAudioEvent) error {
	return t.publishTrackedJob(ctx, func(tx *gorm.DB) error {
		return t.RegisterTranscodeAudio(ctx, tx, job)
	}, func(ctx context.Context) error {
		return t.publisher.PublishTranscodeAudio(ctx, job)
	})
}

// RegisterTranscodeAudio stores the immutable audio allocation in the caller's
// transaction. The caller must enqueue the same command through that
// transaction before committing it.
func (t *Tracker) RegisterTranscodeAudio(ctx context.Context, tx *gorm.DB, job *managev1.TranscodeAudioEvent) error {
	if job == nil {
		return errors.New("audio transcode job is nil")
	}
	if err := ValidateAudioTranscodeEvent(job); err != nil {
		return err
	}
	return t.registerTrackedJob(ctx, tx,
		eventpkg.QueueTranscoderAudio,
		job.EventId,
		job.EntityType.String(),
		job.EntityId,
		job.FileId,
		job,
	)
}

// PublishTranscodeVideo implements mq.TranscoderPublisher with DB tracking.
func (t *Tracker) PublishTranscodeVideo(ctx context.Context, job *managev1.TranscodeVideoEvent) error {
	return t.publishTrackedJob(ctx, func(tx *gorm.DB) error {
		return t.RegisterTranscodeVideo(ctx, tx, job)
	}, func(ctx context.Context) error {
		return t.publisher.PublishTranscodeVideo(ctx, job)
	})
}

// RegisterTranscodeVideo stores the immutable video allocation in the caller's
// transaction. The caller must enqueue the same command through that
// transaction before committing it.
func (t *Tracker) RegisterTranscodeVideo(ctx context.Context, tx *gorm.DB, job *managev1.TranscodeVideoEvent) error {
	if job == nil {
		return errors.New("video transcode job is nil")
	}
	if err := ValidateVideoTranscodeEvent(job); err != nil {
		return err
	}
	return t.registerTrackedJob(ctx, tx,
		eventpkg.QueueTranscoderVideo,
		job.EventId,
		job.EntityType.String(),
		job.EntityId,
		job.FileId,
		job,
	)
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
	register func(*gorm.DB) error,
	publish func(context.Context) error,
) error {
	if err := t.db.WithContext(ctx).Transaction(register); err != nil {
		return err
	}

	// A publisher-confirm error is deliberately not mirrored into PostgreSQL.
	// The synchronous caller retries this same immutable message ID; duplicate
	// broker delivery is handled by the consumer's idempotent output contract.
	return publish(ctx)
}

func (t *Tracker) registerTrackedJob(
	ctx context.Context,
	tx *gorm.DB,
	queueName, eventID, entityType, entityID, fileID string,
	msg proto.Message,
) error {
	if tx == nil {
		return errors.New("transcode job transaction is required")
	}
	payload, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal transcode payload: %w", err)
	}

	now := time.Now()
	record := model.TranscodeJob{
		EventID:    eventID,
		QueueName:  queueName,
		EntityType: entityType,
		EntityID:   entityID,
		FileID:     fileID,
		Payload:    payload,
		Status:     StatusQueued,
		Progress:   0,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	result := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&record)
	if result.Error != nil {
		return fmt.Errorf("save transcode job %s: %w", eventID, result.Error)
	}
	if result.RowsAffected == 1 {
		if err := tx.Model(&model.TranscodeJob{}).
			Where("event_id <> ? AND queue_name = ? AND file_id = ? AND status IN ?", eventID, queueName, fileID, activeTranscodeStatuses()).
			Updates(structured.Fields{
				"status":       StatusCancelled,
				"last_error":   "superseded by newer transcode request",
				"updated_at":   now,
				"completed_at": now,
			}).Error; err != nil {
			return fmt.Errorf("supersede previous transcode jobs: %w", err)
		}
		return nil
	}

	var existing model.TranscodeJob
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("event_id = ?", eventID).
		Take(&existing).Error; err != nil {
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

	now := time.Now()
	return t.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job model.TranscodeJob
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("event_id = ? AND status NOT IN ?", event.EventId, []string{
				StatusCompleted,
				StatusCancelled,
				StatusFailedTerminal,
			}).
			Take(&job).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}

		return tx.Model(&model.TranscodeJob{}).
			Where("event_id = ?", event.EventId).
			Updates(transcodeProgressUpdates(job, event, now)).Error
	})
}

func transcodeProgressUpdates(
	job model.TranscodeJob,
	event *managev1.TranscodeProgressEvent,
	now time.Time,
) structured.Fields {
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

	updates := structured.Fields{
		"status":               StatusProcessing,
		"progress":             maxInt(job.Progress, progress),
		"hls_progress":         hlsProgress,
		"spectrogram_progress": spectrogramProgress,
		"updated_at":           now,
	}
	if isFreshSequence && event.SequenceNumber > 0 {
		updates["last_sequence"] = event.SequenceNumber
	}
	if isFreshSequence && event.Stage != nil {
		stage := event.GetStage().String()
		updates["last_stage"] = stage
	}
	return updates
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
			hlsProgress = maxInt(hlsProgress, stageProgress(progress, 0, 10))
		case managev1.TranscodeStage_TRANSCODE_STAGE_PROCESSING,
			managev1.TranscodeStage_TRANSCODE_STAGE_HLS_PROCESSING:
			hlsProgress = maxInt(hlsProgress, stageProgress(progress, 10, 90))
		case managev1.TranscodeStage_TRANSCODE_STAGE_UPLOADING,
			managev1.TranscodeStage_TRANSCODE_STAGE_HLS_UPLOADING:
			hlsProgress = maxInt(hlsProgress, stageProgress(progress, 90, 100))
		}
		return hlsProgress, spectrogramProgress
	}

	switch stage {
	case managev1.TranscodeStage_TRANSCODE_STAGE_DOWNLOADING:
		sharedProgress := stageProgress(progress, 0, 10)
		hlsProgress = maxInt(hlsProgress, sharedProgress)
		spectrogramProgress = maxInt(spectrogramProgress, sharedProgress)
	case managev1.TranscodeStage_TRANSCODE_STAGE_SPECTROGRAM_PROCESSING:
		hlsProgress = maxInt(hlsProgress, 10)
		spectrogramProgress = maxInt(spectrogramProgress, stageProgress(progress, 10, 85))
	case managev1.TranscodeStage_TRANSCODE_STAGE_SPECTROGRAM_UPLOADING:
		hlsProgress = maxInt(hlsProgress, 10)
		spectrogramProgress = maxInt(spectrogramProgress, stageProgress(progress, 85, 100))
	case managev1.TranscodeStage_TRANSCODE_STAGE_PROCESSING,
		managev1.TranscodeStage_TRANSCODE_STAGE_HLS_PROCESSING:
		hlsProgress = maxInt(hlsProgress, stageProgress(progress, 10, 90))
		spectrogramProgress = maxInt(spectrogramProgress, 100)
	case managev1.TranscodeStage_TRANSCODE_STAGE_UPLOADING,
		managev1.TranscodeStage_TRANSCODE_STAGE_HLS_UPLOADING:
		hlsProgress = maxInt(hlsProgress, stageProgress(progress, 90, 100))
		spectrogramProgress = maxInt(spectrogramProgress, 100)
	}

	return hlsProgress, spectrogramProgress
}

func stageProgress(progress int, start int, end int) int {
	if progress < 0 {
		progress = 0
	}
	if progress > 100 {
		progress = 100
	}
	return start + (((end-start)*progress + 50) / 100)
}

func maxInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
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
	if event.Success {
		job, err := t.matchingTrackedCompletionJob(ctx, event, queueName)
		if err != nil {
			return err
		}
		if job == nil {
			return nil
		}
		return t.db.WithContext(ctx).Where("event_id = ?", job.EventID).Delete(&model.TranscodeJob{}).Error
	}

	jobs, err := t.trackedCompletionJobs(ctx, event, queueName)
	if err != nil {
		return err
	}
	if len(jobs) != 1 || !stringInSlice(jobs[0].Status, activeTranscodeStatuses()) {
		slog.Warn("Skipped uncorrelated transcode failure completion",
			"entityType", event.EntityType.String(),
			"entityId", event.EntityId,
			"fileId", event.FileId,
			"candidateJobs", len(jobs),
		)
		return nil
	}
	job := jobs[0]

	errText := "transcode failed"
	if event.Error != nil && strings.TrimSpace(*event.Error) != "" {
		errText = *event.Error
	}
	return t.db.WithContext(ctx).
		Model(&model.TranscodeJob{}).
		Where("event_id = ?", job.EventID).
		Updates(structured.Fields{
			"status":       StatusFailedTerminal,
			"last_error":   errText,
			"updated_at":   now,
			"completed_at": now,
		}).Error
}

func (t *Tracker) matchingTrackedCompletionJob(
	ctx context.Context,
	event *managev1.TranscodeCompleteEvent,
	queueName string,
) (*model.TranscodeJob, error) {
	jobs, err := t.trackedCompletionJobs(ctx, event, queueName)
	if err != nil {
		return nil, err
	}
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

func (t *Tracker) trackedCompletionJobs(
	ctx context.Context,
	event *managev1.TranscodeCompleteEvent,
	queueName string,
) ([]model.TranscodeJob, error) {
	query := t.db.WithContext(ctx).
		Where("event_id = ? AND entity_type = ? AND entity_id = ? AND file_id = ?", event.EventId, event.EntityType.String(), event.EntityId, event.FileId)
	if queueName != "" {
		query = query.Where("queue_name = ?", queueName)
	}
	var jobs []model.TranscodeJob
	if err := query.Order("created_at DESC").Find(&jobs).Error; err != nil {
		return nil, err
	}
	return jobs, nil
}

func stringInSlice(value string, values []string) bool {
	return slices.Contains(values, value)
}

// MarkCancelled marks active tracked jobs for a file as cancelled.
func (t *Tracker) MarkCancelled(ctx context.Context, fileID string, reason managev1.TranscodeCancelReason) error {
	if strings.TrimSpace(fileID) == "" {
		return nil
	}

	now := time.Now()
	return t.db.WithContext(ctx).
		Model(&model.TranscodeJob{}).
		Where("file_id = ? AND status IN ?", fileID, activeTranscodeStatuses()).
		Updates(transcodeCancelledUpdates(reason, now)).Error
}

func transcodeCancelledUpdates(reason managev1.TranscodeCancelReason, now time.Time) structured.Fields {
	errText := fmt.Sprintf("cancelled: %s", reason.String())
	return structured.Fields{
		"status":       StatusCancelled,
		"last_error":   errText,
		"updated_at":   now,
		"completed_at": now,
	}
}

func activeTranscodeStatuses() []string {
	return []string{
		StatusQueued,
		StatusProcessing,
	}
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
