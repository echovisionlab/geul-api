// Package transcodepostgres is the PostgreSQL store of transcode and
// waveform job state, and the reads other domains make of it in their own
// transactions.
package transcodepostgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/transcode"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// NewUnits returns the transcode unit of work over db.
func NewUnits(db *gorm.DB) mutation.UnitOfWork[transcode.Store] {
	return mutation.Postgres(db, Bind)
}

// Bind binds the transcode store to tx, the transaction of a caller that
// joins it.
func Bind(tx *gorm.DB) transcode.Store {
	return store{tx: tx}
}

// Tracker is the transcode tracker plus the registration other domains run
// in their own transaction.
type Tracker struct {
	*transcode.Tracker
}

func NewTracker(db *gorm.DB, publisher mq.TranscoderPublisher) *Tracker {
	if db == nil {
		panic("transcode.NewTracker: db is required")
	}
	return &Tracker{Tracker: transcode.NewTracker(NewUnits(db), publisher)}
}

// RegisterTranscodeAudio stores the immutable audio allocation in the caller's
// transaction. The caller must enqueue the same command through that
// transaction before committing it.
func (t *Tracker) RegisterTranscodeAudio(ctx context.Context, tx *gorm.DB, job *managev1.TranscodeAudioEvent) error {
	if tx == nil {
		return errors.New("transcode job transaction is required")
	}
	return transcode.RegisterAudio(ctx, Bind(tx), job)
}

// RegisterTranscodeVideo stores the immutable video allocation in the caller's
// transaction. The caller must enqueue the same command through that
// transaction before committing it.
func (t *Tracker) RegisterTranscodeVideo(ctx context.Context, tx *gorm.DB, job *managev1.TranscodeVideoEvent) error {
	if tx == nil {
		return errors.New("transcode job transaction is required")
	}
	return transcode.RegisterVideo(ctx, Bind(tx), job)
}

// CancelTrackWaveformJobs cancels the queued waveform jobs of Tracks in tx,
// the transaction that removes them.
func CancelTrackWaveformJobs(
	ctx context.Context,
	tx *gorm.DB,
	publisher transcode.WaveformCancelPublisher,
	trackIDs []string,
	reason managev1.TranscodeCancelReason,
) error {
	return transcode.CancelTrackWaveformJobs(ctx, Bind(tx), publisher, trackIDs, reason)
}

type store struct {
	tx *gorm.DB
}

func (s store) InsertJob(ctx context.Context, job transcode.Job) (bool, error) {
	record := jobRow(job)
	result := s.tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&record)
	return result.RowsAffected == 1, result.Error
}

func (s store) CancelOtherActiveJobs(ctx context.Context, eventID, queueName, fileID, reason string, now time.Time) error {
	return s.tx.WithContext(ctx).Model(&model.TranscodeJob{}).
		Where("event_id <> ? AND queue_name = ? AND file_id = ? AND status IN ?", eventID, queueName, fileID, transcode.ActiveStatuses()).
		Updates(structured.Fields{
			"status":       transcode.StatusCancelled,
			"last_error":   reason,
			"updated_at":   now,
			"completed_at": now,
		}).Error
}

func (s store) LockJob(ctx context.Context, eventID string) (transcode.Job, error) {
	var job model.TranscodeJob
	if err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("event_id = ?", eventID).
		Take(&job).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return transcode.Job{}, fmt.Errorf("%w: %w", transcode.ErrJobNotFound, err)
		}
		return transcode.Job{}, err
	}
	return jobFromRow(job), nil
}

func (s store) LockUnsettledJob(ctx context.Context, eventID string) (transcode.Job, bool, error) {
	var job model.TranscodeJob
	err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("event_id = ? AND status NOT IN ?", eventID, []string{
			transcode.StatusCompleted,
			transcode.StatusCancelled,
			transcode.StatusFailedTerminal,
		}).
		Take(&job).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return transcode.Job{}, false, nil
	}
	if err != nil {
		return transcode.Job{}, false, err
	}
	return jobFromRow(job), true, nil
}

func (s store) RecordProgress(ctx context.Context, eventID string, progress transcode.Progress, now time.Time) error {
	updates := structured.Fields{
		"status":               transcode.StatusProcessing,
		"progress":             progress.Progress,
		"hls_progress":         progress.HLSProgress,
		"spectrogram_progress": progress.SpectrogramProgress,
		"updated_at":           now,
	}
	if progress.LastSequence != nil {
		updates["last_sequence"] = *progress.LastSequence
	}
	if progress.LastStage != nil {
		updates["last_stage"] = *progress.LastStage
	}
	return s.tx.WithContext(ctx).Model(&model.TranscodeJob{}).Where("event_id = ?", eventID).Updates(updates).Error
}

func (s store) CompletionCandidates(ctx context.Context, identity transcode.CompletionIdentity) ([]transcode.Job, error) {
	var rows []model.TranscodeJob
	if err := s.tx.WithContext(ctx).
		Where("event_id = ? AND entity_type = ? AND entity_id = ? AND file_id = ?",
			identity.EventID, identity.EntityType, identity.EntityID, identity.FileID).
		Where("queue_name = ?", identity.QueueName).
		Order("created_at DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	jobs := make([]transcode.Job, len(rows))
	for i, row := range rows {
		jobs[i] = jobFromRow(row)
	}
	return jobs, nil
}

func (s store) DeleteJob(ctx context.Context, eventID string) error {
	return s.tx.WithContext(ctx).Where("event_id = ?", eventID).Delete(&model.TranscodeJob{}).Error
}

func (s store) FailJob(ctx context.Context, eventID, reason string, now time.Time) error {
	return s.tx.WithContext(ctx).Model(&model.TranscodeJob{}).Where("event_id = ?", eventID).
		Updates(structured.Fields{
			"status":       transcode.StatusFailedTerminal,
			"last_error":   reason,
			"updated_at":   now,
			"completed_at": now,
		}).Error
}

func (s store) CancelFileJobs(ctx context.Context, fileID, reason string, now time.Time) error {
	return s.tx.WithContext(ctx).Model(&model.TranscodeJob{}).
		Where("file_id = ? AND status IN ?", fileID, transcode.ActiveStatuses()).
		Updates(structured.Fields{
			"status":       transcode.StatusCancelled,
			"last_error":   reason,
			"updated_at":   now,
			"completed_at": now,
		}).Error
}

func (s store) QueuedTrackWaveformJobs(ctx context.Context, trackIDs []string) ([]transcode.WaveformJob, error) {
	var rows []model.WaveformJob
	if err := s.tx.WithContext(ctx).
		Where("entity_type = ? AND entity_id IN ? AND status = ?",
			managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK.String(), trackIDs, transcode.WaveformJobStatusQueued).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	jobs := make([]transcode.WaveformJob, len(rows))
	for i, row := range rows {
		jobs[i] = waveformJobFromRow(row)
	}
	return jobs, nil
}

func (s store) CancelWaveformJobs(ctx context.Context, eventIDs []string, reason string, now time.Time) error {
	return s.tx.WithContext(ctx).Model(&model.WaveformJob{}).
		Where("event_id IN ?", eventIDs).
		Updates(structured.Fields{
			"status":           transcode.WaveformJobStatusCancelled,
			"cancel_requested": true,
			"last_error":       reason,
			"completed_at":     now,
			"updated_at":       now,
		}).Error
}

func jobRow(job transcode.Job) model.TranscodeJob {
	return model.TranscodeJob{
		EventID: job.EventID, QueueName: job.QueueName, EntityType: job.EntityType, EntityID: job.EntityID,
		FileID: job.FileID, Payload: job.Payload, Status: job.Status, Progress: job.Progress,
		HLSProgress: job.HLSProgress, SpectrogramProgress: job.SpectrogramProgress,
		LastSequence: job.LastSequence, LastStage: job.LastStage, LastError: job.LastError,
		CompletedAt: job.CompletedAt, CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt,
	}
}

func jobFromRow(row model.TranscodeJob) transcode.Job {
	return transcode.Job{
		EventID: row.EventID, QueueName: row.QueueName, EntityType: row.EntityType, EntityID: row.EntityID,
		FileID: row.FileID, Payload: row.Payload, Status: row.Status, Progress: row.Progress,
		HLSProgress: row.HLSProgress, SpectrogramProgress: row.SpectrogramProgress,
		LastSequence: row.LastSequence, LastStage: row.LastStage, LastError: row.LastError,
		CompletedAt: row.CompletedAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func waveformJobFromRow(row model.WaveformJob) transcode.WaveformJob {
	return transcode.WaveformJob{
		EventID: row.EventID, EntityType: row.EntityType, EntityID: row.EntityID, FileID: row.FileID,
		Status: row.Status, Progress: row.Progress, CancelRequested: row.CancelRequested,
		LastError: row.LastError, CompletedAt: row.CompletedAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}
