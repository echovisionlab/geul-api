// Package applicationpostgres is the PostgreSQL store transcode and waveform
// results apply to: Files, their derivatives, Tracks, and the media asset
// lifecycle.
package applicationpostgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/echovisionlab/geul-api/internal/adapters/filemediaqueue"

	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/transcode"
	"github.com/echovisionlab/geul-api/internal/transcode/application"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// NewUnits returns the transcode application's unit of work over db.
func NewUnits(db *gorm.DB) mutation.UnitOfWork[application.Store] {
	return mutation.Postgres(db, func(tx *gorm.DB) application.Store { return store{tx: tx} })
}

type store struct {
	tx *gorm.DB
}

func (s store) lifecycle() *mediaasset.Lifecycle {
	return mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(s.tx), "")
}

func (s store) FileAcceptsResults(ctx context.Context, fileID string) (bool, error) {
	var count int64
	if err := s.tx.WithContext(ctx).
		Model(&model.File{}).
		Where("id = ? AND delete_requested_at IS NULL", fileID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s store) LockFileAcceptingResults(ctx context.Context, fileID string) (bool, error) {
	var file model.File
	if err := s.tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id").
		Where("id = ? AND delete_requested_at IS NULL", fileID).
		Take(&file).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s store) ProcessingFile(ctx context.Context, fileID string) (application.File, error) {
	var file model.File
	if err := s.tx.WithContext(ctx).Where("id = ?", fileID).Take(&file).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return application.File{}, fmt.Errorf("%w: %w", application.ErrFileNotFound, err)
		}
		return application.File{}, err
	}
	return application.File{
		ID: file.ID, MimeType: file.MimeType, DurationSeconds: file.DurationSeconds,
		IngestSlotID: file.IngestSlotID, IngestAttemptID: file.IngestAttemptID,
	}, nil
}

func (s store) SetFileDuration(ctx context.Context, fileID string, seconds int) error {
	return s.tx.WithContext(ctx).
		Model(&model.File{}).
		Where("id = ?", fileID).
		Update("duration_seconds", seconds).
		Error
}

func (s store) WaveformSource(ctx context.Context, fileID string) (*commonv1.MediaObjectTarget, bool, error) {
	var source model.File
	if err := s.tx.WithContext(ctx).
		Select("id", "extension", "mime_type").
		Where("id = ? AND delete_requested_at IS NULL", fileID).
		Take(&source).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("lookup waveform source: %w", err)
	}
	target, err := filemediaqueue.CanonicalObjectTarget(filemedia.File(source))
	if err != nil {
		return nil, false, fmt.Errorf("build waveform source target: %w", err)
	}
	return target, true, nil
}

func (s store) TranscodeJob(ctx context.Context, identity transcode.CompletionIdentity, lock bool) (*transcode.Job, error) {
	var job model.TranscodeJob
	query := s.tx.WithContext(ctx)
	if lock {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	if err := query.
		Select("event_id", "status", "payload").
		Where("event_id = ? AND queue_name = ? AND entity_type = ? AND entity_id = ? AND file_id = ?",
			identity.EventID, identity.QueueName, identity.EntityType, identity.EntityID, identity.FileID).
		Take(&job).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &transcode.Job{EventID: job.EventID, Status: job.Status, Payload: job.Payload}, nil
}

func (s store) FailActiveTranscodeJob(ctx context.Context, eventID, reason string, now time.Time) (bool, error) {
	result := s.tx.WithContext(ctx).
		Model(&model.TranscodeJob{}).
		Where("event_id = ? AND status IN ?", eventID, transcode.ActiveStatuses()).
		Updates(structured.Fields{
			"status":       transcode.StatusFailedTerminal,
			"last_error":   reason,
			"completed_at": now,
			"updated_at":   now,
		})
	return result.RowsAffected == 1, result.Error
}

func (s store) CancelActiveTranscodeJob(ctx context.Context, eventID, reason string, now time.Time) error {
	return s.tx.WithContext(ctx).Model(&model.TranscodeJob{}).
		Where("event_id = ? AND status IN ?", eventID, transcode.ActiveStatuses()).
		Updates(structured.Fields{
			"status":       transcode.StatusCancelled,
			"last_error":   reason,
			"updated_at":   now,
			"completed_at": now,
		}).Error
}

func (s store) DeleteTranscodeJob(ctx context.Context, eventID, fileID string) error {
	return s.tx.WithContext(ctx).
		Where("event_id = ? AND file_id = ?", eventID, fileID).
		Delete(&model.TranscodeJob{}).Error
}

func (s store) FailedTranscodeJobs(ctx context.Context, fileID, queueName string) (int64, error) {
	var total int64
	err := s.tx.WithContext(ctx).
		Model(&model.TranscodeJob{}).
		Where("file_id = ? AND queue_name = ? AND status = ?", fileID, queueName, transcode.StatusFailedTerminal).
		Count(&total).Error
	return total, err
}

func (s store) FileTranscodeJobs(ctx context.Context, fileID string) ([]transcode.Job, error) {
	var rows []model.TranscodeJob
	if err := s.tx.WithContext(ctx).Where("file_id = ?", fileID).Find(&rows).Error; err != nil {
		return nil, err
	}
	jobs := make([]transcode.Job, len(rows))
	for i, row := range rows {
		jobs[i] = transcode.Job{
			EventID: row.EventID, QueueName: row.QueueName, FileID: row.FileID, Status: row.Status,
			Progress: row.Progress, HLSProgress: row.HLSProgress, SpectrogramProgress: row.SpectrogramProgress,
		}
	}
	return jobs, nil
}

func (s store) SaveWaveformJob(ctx context.Context, job transcode.WaveformJob) error {
	record := model.WaveformJob{
		EventID: job.EventID, EntityType: job.EntityType, EntityID: job.EntityID, FileID: job.FileID,
		Status: job.Status, Progress: job.Progress, CancelRequested: job.CancelRequested,
		CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt,
	}
	return s.tx.WithContext(ctx).Save(&record).Error
}

func (s store) WaveformJob(ctx context.Context, eventID string) (*transcode.WaveformJob, error) {
	var job model.WaveformJob
	if err := s.tx.WithContext(ctx).Where("event_id = ?", eventID).First(&job).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return new(waveformJobFromRow(job)), nil
}

func (s store) DeleteWaveformJob(ctx context.Context, eventID string) error {
	return s.tx.WithContext(ctx).Where("event_id = ?", eventID).Delete(&model.WaveformJob{}).Error
}

func (s store) DeleteFileWaveformJob(ctx context.Context, eventID, fileID string) error {
	return s.tx.WithContext(ctx).
		Where("event_id = ? AND file_id = ?", eventID, fileID).
		Delete(&model.WaveformJob{}).Error
}

func (s store) FailWaveformJob(ctx context.Context, eventID, reason string, now time.Time) error {
	return s.tx.WithContext(ctx).
		Model(&model.WaveformJob{}).
		Where("event_id = ?", eventID).
		Updates(structured.Fields{
			"status":       transcode.WaveformJobStatusFailed,
			"last_error":   reason,
			"completed_at": now,
			"updated_at":   now,
		}).Error
}

func (s store) CancelWaveformJob(ctx context.Context, eventID, reason string, now time.Time) error {
	return s.tx.WithContext(ctx).
		Model(&model.WaveformJob{}).
		Where("event_id = ?", eventID).
		Updates(structured.Fields{
			"status":           transcode.WaveformJobStatusCancelled,
			"cancel_requested": true,
			"last_error":       reason,
			"completed_at":     now,
			"updated_at":       now,
		}).Error
}

func (s store) RecordWaveformProgress(ctx context.Context, eventID string, progress int, sequence int64, stage string, now time.Time) error {
	return s.tx.WithContext(ctx).Exec(`
UPDATE waveform_job
SET progress = GREATEST(progress, ?),
    last_sequence = CASE
        WHEN ? <= 0 THEN last_sequence
        WHEN last_sequence IS NULL THEN ?
        WHEN last_sequence < ? THEN ?
        ELSE last_sequence
    END,
    last_stage = CASE
        WHEN ? = '' THEN last_stage
        WHEN ? <= 0 THEN ?
        WHEN last_sequence IS NULL THEN ?
        WHEN last_sequence < ? THEN ?
        ELSE last_stage
    END,
    updated_at = ?
WHERE event_id = ?
  AND status = ?
  AND cancel_requested = false
`,
		progress,
		sequence,
		sequence,
		sequence,
		sequence,
		stage,
		sequence,
		stage,
		stage,
		sequence,
		stage,
		now,
		eventID,
		transcode.WaveformJobStatusQueued,
	).Error
}

func (s store) QueuedWaveformJobs(ctx context.Context, fileID string) (int64, error) {
	var count int64
	err := s.tx.WithContext(ctx).
		Model(&model.WaveformJob{}).
		Where("file_id = ? AND status = ? AND cancel_requested = false", fileID, transcode.WaveformJobStatusQueued).
		Count(&count).Error
	return count, err
}

func (s store) FailedWaveformJobs(ctx context.Context, fileID string) (int64, error) {
	var count int64
	err := s.tx.WithContext(ctx).
		Model(&model.WaveformJob{}).
		Where("file_id = ? AND status = ? AND cancel_requested = false", fileID, transcode.WaveformJobStatusFailed).
		Count(&count).Error
	return count, err
}

func (s store) FileWaveformJobs(ctx context.Context, fileID string) ([]transcode.WaveformJob, error) {
	var rows []model.WaveformJob
	if err := s.tx.WithContext(ctx).Where("file_id = ?", fileID).Find(&rows).Error; err != nil {
		return nil, err
	}
	jobs := make([]transcode.WaveformJob, len(rows))
	for i, row := range rows {
		jobs[i] = waveformJobFromRow(row)
	}
	return jobs, nil
}

func (s store) DerivativeAsset(ctx context.Context, fileID, derivativeType string) (*string, bool, error) {
	var derivative struct {
		AssetID *string `gorm:"column:asset_id"`
	}
	if err := s.tx.WithContext(ctx).Table("file_derivative").
		Select("asset_id").
		Where("file_id = ? AND type = ?", fileID, derivativeType).
		Take(&derivative).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return derivative.AssetID, true, nil
}

func (s store) DerivativeCount(ctx context.Context, fileID, derivativeType string) (int64, error) {
	var count int64
	err := s.tx.WithContext(ctx).
		Table("file_derivative").
		Where("file_id = ? AND type = ?", fileID, derivativeType).
		Count(&count).Error
	return count, err
}

func (s store) DerivativeTypes(ctx context.Context, fileID string, types []string) ([]string, error) {
	var derivativeTypes []string
	err := s.tx.WithContext(ctx).
		Table("file_derivative").
		Where("file_id = ? AND type IN ?", fileID, types).
		Pluck("type", &derivativeTypes).Error
	return derivativeTypes, err
}

func (s store) UpsertDerivative(ctx context.Context, derivative application.Derivative) error {
	row := model.FileDerivative{
		ID: derivative.ID, FileID: derivative.FileID, Type: managev1.FileDerivativeType(managev1.FileDerivativeType_value[derivative.Type]),
		AssetID: derivative.AssetID, MediaGenerationID: derivative.MediaGenerationID,
	}
	return s.tx.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "file_id"}, {Name: "type"}},
		DoUpdates: clause.Assignments(structured.Fields{
			"asset_id":            row.AssetID,
			"media_generation_id": row.MediaGenerationID,
		}),
	}).Create(&row).Error
}

func (s store) DeleteDerivative(ctx context.Context, fileID, derivativeType string) error {
	return s.tx.WithContext(ctx).Table("file_derivative").
		Where("file_id = ? AND type = ?", fileID, derivativeType).
		Delete(nil).Error
}

func (s store) ReadyDerivatives(ctx context.Context, fileID string) ([]application.Derivative, error) {
	var rows []struct {
		Type              string  `gorm:"column:type"`
		AssetID           *string `gorm:"column:asset_id"`
		MediaGenerationID *string `gorm:"column:media_generation_id"`
	}
	if err := s.tx.WithContext(ctx).
		Table("file_derivative AS fd").
		Select("fd.type", "fd.asset_id", "fd.media_generation_id").
		Joins("LEFT JOIN public_asset AS pa ON pa.id = fd.asset_id").
		Joins("LEFT JOIN media_generation AS mg ON mg.id = fd.media_generation_id").
		Where("fd.file_id = ?", fileID).
		Where(`(
			(fd.asset_id IS NOT NULL AND pa.status = ?)
			OR (fd.media_generation_id IS NOT NULL AND mg.status = ?)
		)`, model.PublicAssetStatusReady, model.MediaGenerationStatusReady).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	derivatives := make([]application.Derivative, len(rows))
	for i, row := range rows {
		derivatives[i] = application.Derivative{
			FileID: fileID, Type: row.Type, AssetID: row.AssetID, MediaGenerationID: row.MediaGenerationID,
		}
	}
	return derivatives, nil
}

func (s store) AssetDerivativeCount(ctx context.Context, assetID string) (int64, error) {
	var count int64
	err := s.tx.WithContext(ctx).Table("file_derivative").Where("asset_id = ?", assetID).Count(&count).Error
	return count, err
}

func (s store) WaveformAssetReferenced(ctx context.Context, assetID string) (bool, error) {
	var count int64
	if err := s.tx.WithContext(ctx).
		Table("file_derivative").
		Where("type = ? AND asset_id = ?", managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM.String(), assetID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s store) GenerationDerivativeCount(ctx context.Context, generationID string) (int64, error) {
	var count int64
	err := s.tx.WithContext(ctx).Model(&model.FileDerivative{}).
		Where("media_generation_id = ?", generationID).Count(&count).Error
	return count, err
}

func (s store) CompleteAsset(ctx context.Context, result *commonv1.AssetWriteResult) (application.Asset, error) {
	asset, err := s.lifecycle().CompletePublicAsset(ctx, mediaasset.WrittenAssetFromProto(result))
	if err != nil {
		return application.Asset{}, err
	}
	return application.Asset{ID: asset.ID, SourceFileID: asset.SourceFileID, Kind: asset.Kind, Status: string(asset.Status), FileSize: asset.FileSize, SHA256: asset.SHA256}, nil
}

func (s store) RequestAssetDeletion(ctx context.Context, assetID string) error {
	return s.lifecycle().RequestPublicAssetDeletion(ctx, assetID)
}

func (s store) AssetBindingCount(ctx context.Context, assetID string) (int64, error) {
	var count int64
	err := s.tx.WithContext(ctx).Table("public_asset_binding").Where("asset_id = ?", assetID).Count(&count).Error
	return count, err
}

func (s store) Asset(ctx context.Context, assetID string) (application.Asset, bool, error) {
	return s.asset(s.tx.WithContext(ctx), assetID)
}

func (s store) LockAsset(ctx context.Context, assetID string) (application.Asset, bool, error) {
	return s.asset(s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}), assetID)
}

func (s store) asset(query *gorm.DB, assetID string) (application.Asset, bool, error) {
	var asset model.PublicAsset
	if err := query.Where("id = ?", assetID).Take(&asset).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return application.Asset{}, false, nil
		}
		return application.Asset{}, false, err
	}
	return assetFromRow(asset), true, nil
}

func (s store) MarkAssetDeletePending(ctx context.Context, assetID string, now time.Time) error {
	return s.tx.WithContext(ctx).Model(&model.PublicAsset{}).
		Where("id = ? AND status IN ?", assetID, []string{
			model.PublicAssetStatusAllocated,
			model.PublicAssetStatusReady,
			model.PublicAssetStatusFailed,
		}).
		Updates(structured.Fields{
			"status":              model.PublicAssetStatusDeletePending,
			"delete_requested_at": now,
			"updated_at":          now,
		}).Error
}

func (s store) AllocateWaveformOutput(ctx context.Context, fileID string) (*commonv1.AssetWriteTarget, error) {
	_, target, err := mediaasset.AllocationResultProto(s.lifecycle().AllocatePublicAsset(ctx, mediaasset.Allocation{
		SourceFileID: &fileID,
		Kind:         "waveform",
		Extension:    "json",
		MimeType:     "application/json",
		Disposition:  mediaasset.DispositionInline,
	}))
	return target, err
}

func (s store) CompleteMediaGeneration(ctx context.Context, result *commonv1.MediaGenerationWriteResult) (application.MediaGeneration, error) {
	generation, err := s.lifecycle().CompleteMediaGeneration(ctx, mediaasset.WrittenGenerationFromProto(result))
	if err != nil {
		return application.MediaGeneration{}, err
	}
	return application.MediaGeneration{ID: generation.ID, FileID: generation.FileID, Kind: generation.Kind, Status: string(generation.Status)}, nil
}

func (s store) LockMediaGeneration(ctx context.Context, generationID string) (application.MediaGeneration, bool, error) {
	var generation model.MediaGeneration
	if err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", generationID).Take(&generation).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return application.MediaGeneration{}, false, nil
		}
		return application.MediaGeneration{}, false, err
	}
	return generationFromRow(generation), true, nil
}

func (s store) RetireMediaGeneration(ctx context.Context, generationID string, result *commonv1.MediaGenerationWriteResult, now time.Time) error {
	updates := structured.Fields{
		"status":       model.MediaGenerationStatusRetired,
		"retired_at":   now,
		"delete_after": now.Add(7 * time.Hour),
		"updated_at":   now,
	}
	if result != nil {
		updates["manifest_sha256"] = append([]byte(nil), result.GetManifestSha256()...)
		updates["object_count"] = result.GetObjectCount()
		updates["total_size"] = result.GetTotalSize()
		updates["ready_at"] = now
	}
	return s.tx.WithContext(ctx).Model(&model.MediaGeneration{}).
		Where("id = ? AND status IN ?", generationID, []string{
			model.MediaGenerationStatusAllocated,
			model.MediaGenerationStatusReady,
		}).
		Updates(updates).Error
}

func (s store) UpdateTrackProcessing(ctx context.Context, trackID, fileID, status string, durationSeconds *int) (bool, error) {
	updates := structured.Fields{"processing_status": status}
	if durationSeconds != nil {
		updates["duration_seconds"] = *durationSeconds
	}
	result := s.tx.WithContext(ctx).
		Model(&model.Track{}).
		Where("id = ? AND audio_original_file_id = ?", trackID, fileID).
		Updates(updates)
	return result.RowsAffected != 0, result.Error
}

func (s store) TrackSourceMatches(ctx context.Context, trackID, fileID string) (bool, error) {
	var count int64
	if err := s.tx.WithContext(ctx).
		Model(&model.Track{}).
		Where("id = ? AND audio_original_file_id = ?", trackID, fileID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s store) TrackRelease(ctx context.Context, trackID string) (string, bool, error) {
	var track model.Track
	if err := s.tx.WithContext(ctx).
		Select("id", "release_id").
		Where("id = ?", trackID).
		Take(&track).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	return track.ReleaseID, true, nil
}

func waveformJobFromRow(row model.WaveformJob) transcode.WaveformJob {
	return transcode.WaveformJob{
		EventID: row.EventID, EntityType: row.EntityType, EntityID: row.EntityID, FileID: row.FileID,
		Status: row.Status, Progress: row.Progress, CancelRequested: row.CancelRequested,
		LastError: row.LastError, CompletedAt: row.CompletedAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func assetFromRow(asset model.PublicAsset) application.Asset {
	return application.Asset{
		ID: asset.ID, SourceFileID: asset.SourceFileID, Kind: asset.Kind, Status: asset.Status,
		FileSize: asset.FileSize, SHA256: asset.SHA256,
	}
}

func generationFromRow(generation model.MediaGeneration) application.MediaGeneration {
	return application.MediaGeneration{
		ID: generation.ID, FileID: generation.FileID, Kind: generation.Kind, Status: generation.Status,
	}
}
