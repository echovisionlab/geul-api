package application

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	transcodestate "github.com/echovisionlab/geul-api/internal/transcode"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (h *Handlers) HandleWaveformProgress(ctx context.Context, body []byte) error {
	var event managev1.WaveformProgressEvent
	if err := proto.Unmarshal(body, &event); err != nil {
		return fmt.Errorf("failed to unmarshal waveform progress event: %w", err)
	}

	job, err := h.getWaveformJob(ctx, event.EventId)
	if err != nil {
		return fmt.Errorf("failed to load waveform job: %w", err)
	}
	if job != nil && !waveformJobMatchesEvent(job, event.EntityType, event.EntityId, event.FileId) {
		return fmt.Errorf("waveform progress identity does not match queued job")
	}
	exists, err := h.fileAcceptsProcessingResults(ctx, event.FileId)
	if err != nil {
		return fmt.Errorf("failed to check waveform source file: %w", err)
	}
	if !exists {
		slog.Debug("Skipped waveform progress for deleted source file", "eventId", event.EventId, "fileId", event.FileId)
		return nil
	}

	isCurrent, err := h.isCurrentTrackAudioSource(ctx, event.EntityType, event.EntityId, event.FileId)
	if err != nil {
		return fmt.Errorf("failed to validate current track source for waveform progress: %w", err)
	}
	if job == nil || !isCurrent || job.CancelRequested || job.Status != transcodestate.WaveformJobStatusQueued {
		return nil
	}

	progress := min(max(int(event.Progress), 0), 100)

	now := time.Now()
	result := h.db.WithContext(ctx).Exec(`
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
		event.SequenceNumber,
		event.SequenceNumber,
		event.SequenceNumber,
		event.SequenceNumber,
		event.GetStage().String(),
		event.SequenceNumber,
		event.GetStage().String(),
		event.GetStage().String(),
		event.SequenceNumber,
		event.GetStage().String(),
		now,
		event.EventId,
		transcodestate.WaveformJobStatusQueued,
	)
	if result.Error != nil {
		return result.Error
	}

	if err := h.publishMediaProcessingLifecycle(ctx, mediaProcessingPublishOptions{
		EntityType:    event.EntityType,
		EntityID:      event.EntityId,
		FileID:        event.FileId,
		CorrelationID: event.EventId,
		Sequence:      event.SequenceNumber,
		TimestampMs:   event.TimestampMs,
	}); err != nil {
		return fmt.Errorf("publish waveform progress lifecycle: %w", err)
	}

	return nil
}

// HandleWaveformComplete stores a waveform sidecar derivative when the current track source still matches.
func (h *Handlers) HandleWaveformComplete(ctx context.Context, body []byte) error {
	var event managev1.WaveformCompleteEvent
	if err := proto.Unmarshal(body, &event); err != nil {
		return fmt.Errorf("failed to unmarshal waveform complete event: %w", err)
	}
	if event.GetOutput() == nil || event.GetOutput().GetAssetId() == "" {
		return fmt.Errorf("waveform complete output asset_id is required")
	}
	if event.GetEventId() != event.GetOutput().GetAssetId() {
		return fmt.Errorf("waveform completion event_id does not match output asset_id")
	}

	job, err := h.getWaveformJob(ctx, event.EventId)
	if err != nil {
		return fmt.Errorf("failed to load waveform job: %w", err)
	}
	if job != nil && !waveformJobMatchesEvent(job, event.EntityType, event.EntityId, event.FileId) {
		return fmt.Errorf("waveform completion identity does not match queued job")
	}
	exists, err := h.fileAcceptsProcessingResults(ctx, event.FileId)
	if err != nil {
		return fmt.Errorf("failed to check waveform source file: %w", err)
	}
	if !exists {
		slog.Debug("Skipped waveform completion for deleted source file", "eventId", event.EventId, "fileId", event.FileId)
		if err := h.retireUnboundWaveformResult(ctx, event.GetOutput()); err != nil {
			return fmt.Errorf("retire deleted file waveform output: %w", err)
		}
		if err := h.finishDeletedFileWaveformJob(ctx, event.EventId, event.FileId); err != nil {
			return fmt.Errorf("failed to cleanup deleted file waveform jobs: %w", err)
		}
		return nil
	}

	isCurrent, err := h.isCurrentTrackAudioSource(ctx, event.EntityType, event.EntityId, event.FileId)
	if err != nil {
		return fmt.Errorf("failed to validate current track source for waveform complete: %w", err)
	}
	if job == nil {
		slog.Debug("Skipped unknown waveform completion", "eventId", event.EventId, "entityId", event.EntityId, "fileId", event.FileId)
		if err := h.retireUnboundWaveformResult(ctx, event.GetOutput()); err != nil {
			return fmt.Errorf("retire unknown waveform output: %w", err)
		}
		return nil
	}
	if !isCurrent || job.CancelRequested || job.Status == transcodestate.WaveformJobStatusCancelled {
		slog.Debug("Skipped stale waveform completion", "entityId", event.EntityId, "fileId", event.FileId)
		if err := h.retireUnboundWaveformResult(ctx, event.GetOutput()); err != nil {
			return fmt.Errorf("retire stale waveform output: %w", err)
		}
		if err := h.markWaveformJobCancelled(ctx, event.EventId, "stale waveform completion ignored"); err != nil {
			slog.Warn("Failed to mark stale waveform job cancelled", "eventId", event.EventId, "error", err)
		}
		return nil
	}

	if err := h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		acceptsResults, err := lockFileAcceptingProcessingResults(ctx, tx, event.FileId)
		if err != nil {
			return err
		}
		if !acceptsResults {
			return errProcessingSourceUnavailable
		}
		return completeAssetDerivative(
			ctx,
			tx,
			mediaasset.NewLifecycle(tx, ""),
			event.FileId,
			managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM,
			"waveform",
			event.GetOutput(),
		)
	}); err != nil {
		return h.handleWaveformDerivativeCompletionError(ctx, &event, err)
	}

	if event.EntityType == managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK {
		if err := h.refreshTrackAudioProcessingStatus(ctx, event.EntityId, event.FileId); err != nil {
			return fmt.Errorf("failed to refresh track waveform completion state: %w", err)
		}
	}

	if err := h.publishMediaProcessingLifecycle(ctx, mediaProcessingPublishOptions{
		EntityType:    event.EntityType,
		EntityID:      event.EntityId,
		FileID:        event.FileId,
		CorrelationID: event.EventId,
		TimestampMs:   event.TimestampMs,
	}); err != nil {
		return fmt.Errorf("publish waveform complete lifecycle: %w", err)
	}
	if err := h.markWaveformJobCompleted(ctx, event.EventId); err != nil {
		return fmt.Errorf("failed to mark waveform job completed: %w", err)
	}

	return nil
}

func (h *Handlers) handleWaveformDerivativeCompletionError(
	ctx context.Context,
	event *managev1.WaveformCompleteEvent,
	cause error,
) error {
	if !errors.Is(cause, errProcessingSourceUnavailable) {
		return fmt.Errorf("failed to complete waveform derivative: %w", cause)
	}
	if err := h.retireUnboundWaveformResult(ctx, event.GetOutput()); err != nil {
		return fmt.Errorf("retire waveform output rejected by file deletion: %w", err)
	}
	if err := h.finishDeletedFileWaveformJob(ctx, event.EventId, event.FileId); err != nil {
		return fmt.Errorf("cleanup waveform job rejected by file deletion: %w", err)
	}
	return nil
}

func (h *Handlers) retireUnboundWaveformResult(ctx context.Context, result *commonv1.AssetWriteResult) error {
	return h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if result != nil && strings.TrimSpace(result.GetAssetId()) != "" {
			referenced, err := waveformAssetIsReferenced(ctx, tx, result.GetAssetId())
			if err != nil {
				return err
			}
			if referenced {
				return nil
			}
		}
		lifecycle := mediaasset.NewLifecycle(tx, "")
		asset, err := lifecycle.CompletePublicAsset(ctx, result)
		if err != nil {
			retired, lookupErr := waveformResultAlreadyRetired(ctx, tx, result)
			if lookupErr != nil {
				return lookupErr
			}
			if retired {
				return nil
			}
			return err
		}
		if asset.Kind != "waveform" {
			return fmt.Errorf("asset %s is not a waveform", asset.ID)
		}
		return lifecycle.RequestPublicAssetDeletion(ctx, asset.ID)
	})
}

func waveformAssetIsReferenced(ctx context.Context, db *gorm.DB, assetID string) (bool, error) {
	var count int64
	if err := db.WithContext(ctx).
		Table("file_derivative").
		Where("type = ? AND asset_id = ?", managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM.String(), strings.TrimSpace(assetID)).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func waveformResultAlreadyRetired(
	ctx context.Context,
	db *gorm.DB,
	result *commonv1.AssetWriteResult,
) (bool, error) {
	if result == nil || result.GetFileSize() <= 0 || len(result.GetSha256()) != 32 {
		return false, nil
	}
	var asset model.PublicAsset
	if err := db.WithContext(ctx).
		Where("id = ?", strings.TrimSpace(result.GetAssetId())).
		Take(&asset).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	if asset.Kind != "waveform" ||
		(asset.Status != model.PublicAssetStatusDeletePending && asset.Status != model.PublicAssetStatusDeleted) ||
		asset.FileSize == nil {
		return false, nil
	}
	return *asset.FileSize == result.GetFileSize() && bytes.Equal(asset.SHA256, result.GetSha256()), nil
}

// HandleWaveformFail clears stale waveform state only when the current track source still matches.
func (h *Handlers) HandleWaveformFail(ctx context.Context, body []byte) error {
	var event managev1.WaveformFailEvent
	if err := proto.Unmarshal(body, &event); err != nil {
		return fmt.Errorf("failed to unmarshal waveform fail event: %w", err)
	}

	job, err := h.getWaveformJob(ctx, event.EventId)
	if err != nil {
		return fmt.Errorf("failed to load waveform job: %w", err)
	}
	if job != nil && !waveformJobMatchesEvent(job, event.EntityType, event.EntityId, event.FileId) {
		return fmt.Errorf("waveform failure identity does not match queued job")
	}
	exists, err := h.fileAcceptsProcessingResults(ctx, event.FileId)
	if err != nil {
		return fmt.Errorf("failed to check waveform source file: %w", err)
	}
	if !exists {
		slog.Debug("Skipped waveform failure for deleted source file", "eventId", event.EventId, "fileId", event.FileId)
		if err := h.finishDeletedFileWaveformJob(ctx, event.EventId, event.FileId); err != nil {
			return fmt.Errorf("failed to cleanup deleted file waveform jobs: %w", err)
		}
		return nil
	}

	isCurrent, err := h.isCurrentTrackAudioSource(ctx, event.EntityType, event.EntityId, event.FileId)
	if err != nil {
		return fmt.Errorf("failed to validate current track source for waveform fail: %w", err)
	}
	if job == nil {
		slog.Debug("Skipped unknown waveform failure", "eventId", event.EventId, "entityId", event.EntityId, "fileId", event.FileId)
		return nil
	}
	if !isCurrent || job.CancelRequested || job.Status == transcodestate.WaveformJobStatusCancelled {
		slog.Debug("Skipped stale waveform failure", "entityId", event.EntityId, "fileId", event.FileId)
		if err := h.markWaveformJobCancelled(ctx, event.EventId, "stale waveform failure ignored"); err != nil {
			slog.Warn("Failed to mark stale waveform job cancelled", "eventId", event.EventId, "error", err)
		}
		return nil
	}

	if err := h.removeFileDerivative(ctx, event.FileId, managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM); err != nil {
		slog.Warn("Failed to clear waveform derivative after waveform processing failure",
			"entityId", event.EntityId,
			"fileId", event.FileId,
			"error", err,
		)
	}

	slog.Warn("Waveform processing failed",
		"entityId", event.EntityId,
		"fileId", event.FileId,
		"error", event.Error,
	)

	if err := h.markWaveformJobFailed(ctx, event.EventId, event.Error); err != nil {
		return fmt.Errorf("failed to mark waveform job failed: %w", err)
	}

	if event.EntityType == managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK {
		if err := h.refreshTrackAudioProcessingStatus(ctx, event.EntityId, event.FileId); err != nil {
			return fmt.Errorf("failed to refresh track waveform failure state: %w", err)
		}
	}

	if err := h.publishMediaProcessingLifecycle(ctx, mediaProcessingPublishOptions{
		EntityType:    event.EntityType,
		EntityID:      event.EntityId,
		FileID:        event.FileId,
		CorrelationID: event.EventId,
		TimestampMs:   event.TimestampMs,
		Error:         event.Error,
	}); err != nil {
		return fmt.Errorf("publish waveform fail lifecycle: %w", err)
	}

	return nil
}

func (h *Handlers) isCurrentTrackAudioSource(
	ctx context.Context,
	entityType managev1.TranscodeEntityType,
	entityID string,
	fileID string,
) (bool, error) {
	if entityType != managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK {
		return true, nil
	}

	var count int64
	if err := h.db.WithContext(ctx).
		Model(&model.Track{}).
		Where("id = ? AND audio_original_file_id = ?", entityID, fileID).
		Count(&count).Error; err != nil {
		return false, err
	}

	return count > 0, nil
}

func waveformJobMatchesEvent(
	job *model.WaveformJob,
	entityType managev1.TranscodeEntityType,
	entityID string,
	fileID string,
) bool {
	return job != nil &&
		job.EntityType == entityType.String() &&
		job.EntityID == entityID &&
		job.FileID == fileID
}

// HandleTranscodeProgress updates backend transcode job state from progress events.
