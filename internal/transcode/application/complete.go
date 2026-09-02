package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	transcodestate "github.com/echovisionlab/geul-api/internal/transcode"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

var (
	errProcessingSourceUnavailable    = errors.New("processing source no longer accepts results")
	errTranscodeAllocationUnavailable = errors.New("transcode allocation no longer accepts results")
)

// HandleTranscodeComplete handles transcode completion events from the transcoder service.
// It stores derivative files (thumbnails, HLS manifests, audio sidecars) in the file_derivative table.
func (h *Handlers) HandleTranscodeComplete(ctx context.Context, body []byte) error {
	event, err := parseTranscodeCompleteEvent(body)
	if err != nil {
		return err
	}
	logTranscodeComplete(event)

	skipped, err := h.skipUnavailableTranscodeSource(ctx, event)
	if err != nil || skipped {
		return err
	}
	if !event.Success {
		return h.handleFailedTranscodeComplete(ctx, event)
	}
	return h.handleSuccessfulTranscodeComplete(ctx, event)
}

func (h *Handlers) failTrackedTranscodeJob(
	ctx context.Context,
	event *managev1.TranscodeCompleteEvent,
) (bool, error) {
	if event == nil {
		return false, nil
	}
	ownerStateApplied := event.GetEntityType() != managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK
	err := h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		acceptsResults, err := lockFileAcceptingProcessingResults(ctx, tx, event.GetFileId())
		if err != nil {
			return err
		}
		if !acceptsResults {
			return errProcessingSourceUnavailable
		}
		job, err := h.trackedTranscodeJobByIdentityDB(ctx, tx, event, true)
		if err != nil {
			return err
		}
		if job == nil || !transcodeJobAcceptsCompletion(job.Status) {
			return errTranscodeAllocationUnavailable
		}
		now := time.Now().UTC()
		errText := "transcode failed"
		if event.Error != nil && strings.TrimSpace(*event.Error) != "" {
			errText = strings.TrimSpace(*event.Error)
		}
		result := tx.WithContext(ctx).
			Model(&model.TranscodeJob{}).
			Where("event_id = ? AND status IN ?", job.EventID, []string{
				transcodestate.StatusQueued,
				transcodestate.StatusProcessing,
			}).
			Updates(structured.Fields{
				"status":       transcodestate.StatusFailedTerminal,
				"last_error":   errText,
				"completed_at": now,
				"updated_at":   now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errTranscodeAllocationUnavailable
		}
		if event.GetEntityType() == managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK {
			ownerStateApplied, err = h.finalizeTrackTranscodeStateWithDB(ctx, tx, event)
			return err
		}
		return nil
	})
	if errors.Is(err, errProcessingSourceUnavailable) {
		return false, h.finishDeletedFileTranscodeJob(ctx, event)
	}
	if errors.Is(err, errTranscodeAllocationUnavailable) {
		return false, nil
	}
	return ownerStateApplied, err
}

func (h *Handlers) finishTranscodeJob(ctx context.Context, event *managev1.TranscodeCompleteEvent) error {
	if h.transcodeJobs == nil {
		return nil
	}
	if err := h.transcodeJobs.HandleTranscodeComplete(ctx, event); err != nil {
		return fmt.Errorf("failed to update transcode job completion state: %w", err)
	}
	return nil
}

func (h *Handlers) fileAcceptsProcessingResults(ctx context.Context, fileID string) (bool, error) {
	if strings.TrimSpace(fileID) == "" {
		return false, nil
	}

	var count int64
	if err := h.db.WithContext(ctx).
		Model(&model.File{}).
		Where("id = ? AND delete_requested_at IS NULL", fileID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func lockFileAcceptingProcessingResults(ctx context.Context, tx *gorm.DB, fileID string) (bool, error) {
	if tx == nil || strings.TrimSpace(fileID) == "" {
		return false, nil
	}
	var file model.File
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id").
		Where("id = ? AND delete_requested_at IS NULL", strings.TrimSpace(fileID)).
		Take(&file).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (h *Handlers) finishDeletedFileTranscodeJob(
	ctx context.Context,
	event *managev1.TranscodeCompleteEvent,
) error {
	if event == nil || strings.TrimSpace(event.GetFileId()) == "" || strings.TrimSpace(event.GetEventId()) == "" {
		return nil
	}
	return h.db.WithContext(ctx).
		Where("event_id = ? AND file_id = ?", event.GetEventId(), event.GetFileId()).
		Delete(&model.TranscodeJob{}).Error
}

func (h *Handlers) retireStaleTranscodeOutputs(ctx context.Context, event *managev1.TranscodeCompleteEvent) error {
	if event == nil || event.Outputs == nil {
		return nil
	}
	return h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		acceptsResults, err := lockFileAcceptingProcessingResults(ctx, tx, event.GetFileId())
		if err != nil || !acceptsResults {
			return err
		}
		tracked, err := h.trackedTranscodeAllocationForCompletionDB(ctx, tx, event, true)
		if err != nil {
			return err
		}
		if tracked == nil {
			slog.Warn("Skipped stale transcode output retirement without matching allocation",
				"fileId", event.FileId,
				"entityType", event.EntityType.String(),
				"entityId", event.EntityId,
			)
			return nil
		}
		now := time.Now().UTC()
		if transcodeJobAcceptsCompletion(tracked.Status) {
			if err := tx.Model(&model.TranscodeJob{}).
				Where("event_id = ? AND status IN ?", tracked.EventID, []string{
					transcodestate.StatusQueued,
					transcodestate.StatusProcessing,
				}).
				Updates(structured.Fields{
					"status":       transcodestate.StatusCancelled,
					"last_error":   "stale completion no longer owns the entity projection",
					"updated_at":   now,
					"completed_at": now,
				}).Error; err != nil {
				return err
			}
		}
		for _, target := range collectStaleTranscodeOutputTargets(event.Outputs) {
			if target.derivativeType == managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS {
				if err := retireStaleMediaGeneration(ctx, tx, event.GetFileId(), event.Outputs.GetHls(), now); err != nil {
					return err
				}
				continue
			}
			if err := markStalePublicAssetDeletePending(ctx, tx, event.GetFileId(), target.id, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func (h *Handlers) trackedTranscodeAllocationForCompletion(
	ctx context.Context,
	event *managev1.TranscodeCompleteEvent,
) (*model.TranscodeJob, error) {
	return h.trackedTranscodeAllocationForCompletionDB(ctx, h.db, event, false)
}

func (h *Handlers) trackedTranscodeAllocationForCompletionDB(
	ctx context.Context,
	db *gorm.DB,
	event *managev1.TranscodeCompleteEvent,
	lock bool,
) (*model.TranscodeJob, error) {
	job, err := h.trackedTranscodeJobByIdentityDB(ctx, db, event, lock)
	if err != nil || job == nil {
		return job, err
	}
	matches, err := transcodestate.MatchTranscodeCompletionPayload(event, job.Payload)
	if err != nil {
		return nil, err
	}
	if !matches {
		return nil, nil
	}
	return job, nil
}

func (h *Handlers) trackedTranscodeJobByIdentityDB(
	ctx context.Context,
	db *gorm.DB,
	event *managev1.TranscodeCompleteEvent,
	lock bool,
) (*model.TranscodeJob, error) {
	queueName := ""
	switch event.GetEventType() {
	case managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO:
		queueName = eventpkg.QueueTranscoderAudio
	case managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_VIDEO:
		queueName = eventpkg.QueueTranscoderVideo
	default:
		return nil, nil
	}
	var job model.TranscodeJob
	query := db.WithContext(ctx)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.
		Select("event_id", "status", "payload").
		Where("event_id = ? AND queue_name = ? AND entity_type = ? AND entity_id = ? AND file_id = ?",
			event.GetEventId(), queueName, event.GetEntityType().String(), event.GetEntityId(), event.GetFileId()).
		Take(&job).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &job, nil
}

func transcodeJobAcceptsCompletion(status string) bool {
	switch status {
	case transcodestate.StatusQueued,
		transcodestate.StatusProcessing:
		return true
	default:
		return false
	}
}

type staleTranscodeOutputTarget struct {
	derivativeType managev1.FileDerivativeType
	id             string
}

func collectStaleTranscodeOutputTargets(
	outputs *managev1.TranscodeOutputs,
) []staleTranscodeOutputTarget {
	if outputs == nil {
		return nil
	}

	targets := make([]staleTranscodeOutputTarget, 0, 3)
	appendAssetTarget := func(derivativeType managev1.FileDerivativeType, result *commonv1.AssetWriteResult) {
		if result == nil || strings.TrimSpace(result.GetAssetId()) == "" {
			return
		}
		targets = append(targets, staleTranscodeOutputTarget{
			derivativeType: derivativeType,
			id:             strings.TrimSpace(result.GetAssetId()),
		})
	}
	appendHLSTarget := func(result *commonv1.MediaGenerationWriteResult) {
		if result == nil || strings.TrimSpace(result.GetGenerationId()) == "" {
			return
		}
		targets = append(targets, staleTranscodeOutputTarget{
			derivativeType: managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS,
			id:             strings.TrimSpace(result.GetGenerationId()),
		})
	}

	appendAssetTarget(managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_THUMBNAIL, outputs.GetThumbnail())
	appendAssetTarget(managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_SPECTROGRAM, outputs.GetSpectrogram())
	appendHLSTarget(outputs.GetHls())
	return targets
}

func retireStaleMediaGeneration(
	ctx context.Context,
	tx *gorm.DB,
	fileID string,
	result *commonv1.MediaGenerationWriteResult,
	now time.Time,
) error {
	if result == nil {
		return fmt.Errorf("stale media generation result is required")
	}
	generationID := strings.TrimSpace(result.GetGenerationId())
	var generation model.MediaGeneration
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", generationID).Take(&generation).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if generation.FileID != fileID || generation.Kind != "hls" {
		return fmt.Errorf("stale media generation does not belong to source file")
	}
	var derivativeCount int64
	if err := tx.WithContext(ctx).Model(&model.FileDerivative{}).
		Where("media_generation_id = ?", generation.ID).Count(&derivativeCount).Error; err != nil {
		return err
	}
	if derivativeCount != 0 {
		return nil
	}
	if generation.Status == model.MediaGenerationStatusRetired {
		return nil
	}
	updates := structured.Fields{
		"status":       model.MediaGenerationStatusRetired,
		"retired_at":   now,
		"delete_after": now.Add(7 * time.Hour),
		"updated_at":   now,
	}
	if generation.Status == model.MediaGenerationStatusAllocated {
		updates["manifest_sha256"] = append([]byte(nil), result.GetManifestSha256()...)
		updates["object_count"] = result.GetObjectCount()
		updates["total_size"] = result.GetTotalSize()
		updates["ready_at"] = now
	}
	return tx.WithContext(ctx).Model(&model.MediaGeneration{}).
		Where("id = ? AND status IN ?", generation.ID, []string{
			model.MediaGenerationStatusAllocated,
			model.MediaGenerationStatusReady,
		}).
		Updates(updates).Error
}

func markStalePublicAssetDeletePending(
	ctx context.Context,
	tx *gorm.DB,
	fileID string,
	assetID string,
	now time.Time,
) error {
	var asset model.PublicAsset
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", assetID).Take(&asset).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if asset.SourceFileID == nil || *asset.SourceFileID != fileID {
		return fmt.Errorf("stale public asset does not belong to source file")
	}
	var derivativeCount int64
	if err := tx.WithContext(ctx).Model(&model.FileDerivative{}).
		Where("asset_id = ?", asset.ID).Count(&derivativeCount).Error; err != nil {
		return err
	}
	var bindingCount int64
	if err := tx.WithContext(ctx).Model(&model.PublicAssetBinding{}).
		Where("asset_id = ?", asset.ID).Count(&bindingCount).Error; err != nil {
		return err
	}
	if derivativeCount != 0 || bindingCount != 0 ||
		asset.Status == model.PublicAssetStatusDeletePending || asset.Status == model.PublicAssetStatusDeleted {
		return nil
	}
	return tx.WithContext(ctx).Model(&model.PublicAsset{}).
		Where("id = ? AND status IN ?", asset.ID, []string{
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

func (h *Handlers) persistFileDurationSeconds(ctx context.Context, fileID string, durationSeconds int32) error {
	if durationSeconds < 0 {
		return nil
	}
	return h.db.WithContext(ctx).
		Model(&model.File{}).
		Where("id = ?", fileID).
		Update("duration_seconds", int(durationSeconds)).
		Error
}

func (h *Handlers) finalizeTrackTranscodeStateWithDB(
	ctx context.Context,
	db *gorm.DB,
	event *managev1.TranscodeCompleteEvent,
) (bool, error) {
	if event.EntityType != managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK {
		return false, nil
	}

	status, err := h.determineTrackAudioProcessingStatusWithDB(ctx, db, event.FileId)
	if err != nil {
		return false, err
	}

	updates := structured.Fields{
		"processing_status": status,
	}
	if event.Success && event.Outputs != nil && event.Outputs.DurationSeconds != nil {
		updates["duration_seconds"] = int(*event.Outputs.DurationSeconds)
	}

	result := db.WithContext(ctx).
		Model(&model.Track{}).
		Where("id = ? AND audio_original_file_id = ?", event.EntityId, event.FileId).
		Updates(updates)
	if result.Error != nil {
		return false, result.Error
	}

	if result.RowsAffected == 0 {
		slog.Debug("Skipped stale track transcode completion", "trackId", event.EntityId, "fileId", event.FileId)
		return false, nil
	}

	return true, nil
}

func (h *Handlers) publishWaveformGenerate(
	ctx context.Context,
	event *managev1.TranscodeCompleteEvent,
) error {
	if h.publisher == nil ||
		event.EventType != managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO ||
		!event.Success ||
		event.Outputs == nil {
		return nil
	}

	shouldEnqueue, err := h.shouldEnqueueWaveformJob(ctx, event.FileId)
	if err != nil {
		return fmt.Errorf("check waveform enqueue state: %w", err)
	}
	if !shouldEnqueue {
		return nil
	}

	var source model.File
	if err := h.db.WithContext(ctx).
		Select("id", "extension", "mime_type").
		Where("id = ? AND delete_requested_at IS NULL", event.FileId).
		Take(&source).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return fmt.Errorf("lookup waveform source: %w", err)
	}
	sourceTarget, err := filemedia.CanonicalMediaObjectTargetForFile(source)
	if err != nil {
		return fmt.Errorf("build waveform source target: %w", err)
	}
	fileID := strings.TrimSpace(event.FileId)
	_, outputTarget, err := mediaasset.NewLifecycle(h.db, "").AllocatePublicAsset(ctx, mediaasset.Allocation{
		SourceFileID: &fileID,
		Kind:         "waveform",
		Extension:    "json",
		MimeType:     "application/json",
		Disposition:  commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	})
	if err != nil {
		return fmt.Errorf("allocate waveform output: %w", err)
	}

	job := &managev1.WaveformGenerateEvent{
		EventId:    outputTarget.GetAssetId(),
		EntityType: event.EntityType,
		EntityId:   event.EntityId,
		FileId:     event.FileId,
		Source:     sourceTarget,
		Output:     outputTarget,
	}

	if err := h.upsertWaveformJob(ctx, job); err != nil {
		return fmt.Errorf("create waveform job record: %w", err)
	}

	if err := h.publisher.PublishWaveformGenerate(ctx, job); err != nil {
		if markErr := h.markWaveformJobFailed(ctx, job.EventId, err.Error()); markErr != nil {
			slog.Warn("Failed to mark waveform job publish failure", "eventId", job.EventId, "error", markErr)
		}
		return err
	}

	return nil
}
