package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func parseTranscodeCompleteEvent(body []byte) (*managev1.TranscodeCompleteEvent, error) {
	var event managev1.TranscodeCompleteEvent
	if err := proto.Unmarshal(body, &event); err != nil {
		return nil, fmt.Errorf("failed to unmarshal transcode complete event: %w", err)
	}
	if strings.TrimSpace(event.EventId) == "" {
		return nil, fmt.Errorf("transcode completion event_id is required")
	}
	return &event, nil
}

func logTranscodeComplete(event *managev1.TranscodeCompleteEvent) {
	slog.Info("Processing transcode complete event",
		"fileId", event.FileId,
		"entityType", event.EntityType.String(),
		"entityId", event.EntityId,
		"success", event.Success,
		"eventType", event.EventType.String(),
	)
}

func (h *Handlers) skipUnavailableTranscodeSource(
	ctx context.Context,
	event *managev1.TranscodeCompleteEvent,
) (bool, error) {
	acceptsResults, err := h.fileAcceptsProcessingResults(ctx, event.FileId)
	if err != nil {
		return false, fmt.Errorf("failed to check transcode source file: %w", err)
	}
	if acceptsResults {
		return false, nil
	}
	slog.Debug(
		"Skipped transcode completion for deleted or pending-deletion source file",
		"fileId",
		event.FileId,
	)
	if err := h.finishDeletedFileTranscodeJob(ctx, event); err != nil {
		return true, fmt.Errorf("failed to cleanup deleted file processing jobs: %w", err)
	}
	return true, nil
}

func (h *Handlers) handleFailedTranscodeComplete(
	ctx context.Context,
	event *managev1.TranscodeCompleteEvent,
) error {
	ownerStateApplied, err := h.failTrackedTranscodeJob(ctx, event)
	if err != nil {
		return fmt.Errorf("failed to finalize transcode failure: %w", err)
	}
	if !ownerStateApplied {
		return nil
	}
	errMessage := ptrStringValue(event.Error)
	if err := h.publishMediaProcessingLifecycle(ctx, mediaProcessingPublishOptions{
		EntityType:  event.EntityType,
		EntityID:    event.EntityId,
		FileID:      event.FileId,
		TimestampMs: event.TimestampMs,
		Error:       errMessage,
	}); err != nil {
		return fmt.Errorf("failed to publish failed transcode lifecycle: %w", err)
	}
	slog.Warn(
		"Transcode failed, skipping derivative storage",
		"fileId",
		event.FileId,
		"error",
		errMessage,
	)
	return nil
}

func (h *Handlers) handleSuccessfulTranscodeComplete(
	ctx context.Context,
	event *managev1.TranscodeCompleteEvent,
) error {
	if event.Outputs == nil {
		return fmt.Errorf("successful transcode completion outputs are required")
	}
	if err := validateTranscodeCompletionOutputs(event); err != nil {
		return err
	}
	trackedJob, err := h.trackedTranscodeAllocationForCompletion(ctx, event)
	if err != nil {
		return fmt.Errorf("load tracked transcode allocation: %w", err)
	}
	if trackedJob == nil {
		logUntrackedTranscodeComplete(event)
		return nil
	}
	if !transcodeJobAcceptsCompletion(trackedJob.Status) {
		return h.retireSupersededTranscodeComplete(ctx, event)
	}

	trackStateApplied, continueCompletion, err := h.completeAllocatedTranscodeOutputs(ctx, event)
	if err != nil || !continueCompletion {
		return err
	}
	return h.publishSuccessfulTranscodeComplete(ctx, event, trackStateApplied)
}

func logUntrackedTranscodeComplete(event *managev1.TranscodeCompleteEvent) {
	slog.Warn(
		"Skipped untracked transcode completion",
		"fileId",
		event.FileId,
		"entityType",
		event.EntityType.String(),
		"entityId",
		event.EntityId,
	)
}

func (h *Handlers) retireSupersededTranscodeComplete(
	ctx context.Context,
	event *managev1.TranscodeCompleteEvent,
) error {
	if err := h.retireStaleTranscodeOutputs(ctx, event); err != nil {
		return fmt.Errorf("failed to retire superseded transcode outputs: %w", err)
	}
	return h.finishTranscodeJob(ctx, event)
}

func (h *Handlers) completeAllocatedTranscodeOutputs(
	ctx context.Context,
	event *managev1.TranscodeCompleteEvent,
) (bool, bool, error) {
	trackStateApplied := false
	err := h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		acceptsResults, err := lockFileAcceptingProcessingResults(ctx, tx, event.FileId)
		if err != nil {
			return err
		}
		if !acceptsResults {
			return errProcessingSourceUnavailable
		}
		if err := h.requireAcceptingTranscodeAllocation(ctx, tx, event); err != nil {
			return err
		}
		if event.EntityType == managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK {
			trackStateApplied, err = h.finalizeTrackTranscodeStateWithDB(ctx, tx, event)
			if err != nil {
				return err
			}
			if !trackStateApplied {
				return errTranscodeAllocationUnavailable
			}
		}
		return completeTranscodeDerivatives(ctx, tx, event)
	})
	if err == nil {
		return trackStateApplied, true, nil
	}
	continueCompletion, handledErr := h.handleAllocatedTranscodeOutputError(ctx, event, err)
	return false, continueCompletion, handledErr
}

func (h *Handlers) requireAcceptingTranscodeAllocation(
	ctx context.Context,
	tx *gorm.DB,
	event *managev1.TranscodeCompleteEvent,
) error {
	job, err := h.trackedTranscodeAllocationForCompletionDB(ctx, tx, event, true)
	if err != nil {
		return err
	}
	if job == nil || !transcodeJobAcceptsCompletion(job.Status) {
		return errTranscodeAllocationUnavailable
	}
	return nil
}

func completeTranscodeDerivatives(
	ctx context.Context,
	tx *gorm.DB,
	event *managev1.TranscodeCompleteEvent,
) error {
	lifecycle := mediaasset.NewLifecycle(tx, "")
	if event.EventType == managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_VIDEO {
		if err := completeAssetDerivative(
			ctx,
			tx,
			lifecycle,
			event.FileId,
			managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_THUMBNAIL,
			"thumbnail",
			event.Outputs.GetThumbnail(),
		); err != nil {
			return err
		}
	}
	if event.EventType == managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO {
		if err := completeAssetDerivative(
			ctx,
			tx,
			lifecycle,
			event.FileId,
			managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_SPECTROGRAM,
			"spectrogram",
			event.Outputs.GetSpectrogram(),
		); err != nil {
			return err
		}
	}
	return completeGenerationDerivative(
		ctx,
		tx,
		lifecycle,
		event.FileId,
		managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS,
		event.Outputs.GetHls(),
	)
}

func (h *Handlers) handleAllocatedTranscodeOutputError(
	ctx context.Context,
	event *managev1.TranscodeCompleteEvent,
	err error,
) (bool, error) {
	if errors.Is(err, errProcessingSourceUnavailable) {
		if cleanupErr := h.finishDeletedFileTranscodeJob(ctx, event); cleanupErr != nil {
			return false, fmt.Errorf("cleanup transcode jobs rejected by file deletion: %w", cleanupErr)
		}
		return false, nil
	}
	if errors.Is(err, errTranscodeAllocationUnavailable) {
		if retireErr := h.retireStaleTranscodeOutputs(ctx, event); retireErr != nil {
			return false, fmt.Errorf("retire outputs rejected by transcode allocation: %w", retireErr)
		}
		return false, h.finishTranscodeJob(ctx, event)
	}
	return false, fmt.Errorf("complete allocated transcode outputs: %w", err)
}

func (h *Handlers) publishSuccessfulTranscodeComplete(
	ctx context.Context,
	event *managev1.TranscodeCompleteEvent,
	trackStateApplied bool,
) error {
	if event.Outputs.DurationSeconds != nil {
		duration := int32(*event.Outputs.DurationSeconds)
		if err := h.persistFileDurationSeconds(ctx, event.FileId, duration); err != nil {
			return fmt.Errorf("failed to persist file duration: %w", err)
		}
	}
	if shouldPublishWaveformGenerate(event, trackStateApplied) {
		if err := h.publishWaveformGenerate(ctx, event); err != nil {
			return fmt.Errorf("failed to publish waveform generate event: %w", err)
		}
	}
	if err := h.publishMediaProcessingLifecycle(ctx, mediaProcessingPublishOptions{
		EntityType:  event.EntityType,
		EntityID:    event.EntityId,
		FileID:      event.FileId,
		TimestampMs: event.TimestampMs,
	}); err != nil {
		return fmt.Errorf("failed to publish transcode lifecycle: %w", err)
	}
	return h.finishTranscodeJob(ctx, event)
}

func shouldPublishWaveformGenerate(
	event *managev1.TranscodeCompleteEvent,
	trackStateApplied bool,
) bool {
	if event.EventType != managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO {
		return false
	}
	if event.Outputs.GetHls().GetGenerationId() == "" || event.Outputs.GetSpectrogram().GetAssetId() == "" {
		return false
	}
	return event.EntityType != managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK || trackStateApplied
}

func ptrStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
