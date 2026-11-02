package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	transcodestate "github.com/echovisionlab/geul-api/internal/transcode"
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

func logTranscodeComplete(ctx context.Context, event *managev1.TranscodeCompleteEvent) {
	slog.InfoContext(ctx, "Processing transcode complete event",
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
	slog.DebugContext(ctx,
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
	slog.ErrorContext(ctx, "Transcode failed, skipping derivative storage",
		"fileId", event.FileId,
		"eventId", event.EventId,
		"entityType", event.EntityType.String(),
		"entityId", event.EntityId,
		"eventType", event.EventType.String(),
		failurecause.Attr(errors.New(errMessage)),
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
	var trackedJob *transcodestate.Job
	err := h.read(ctx, func(store Store) (err error) {
		trackedJob, err = trackedTranscodeAllocationForCompletion(ctx, store, event, false)
		return err
	})
	if err != nil {
		return fmt.Errorf("load tracked transcode allocation: %w", err)
	}
	if trackedJob == nil {
		logUntrackedTranscodeComplete(ctx, event)
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

func logUntrackedTranscodeComplete(ctx context.Context, event *managev1.TranscodeCompleteEvent) {
	slog.WarnContext(ctx,
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
	err := h.transaction(ctx, func(store Store) error {
		acceptsResults, err := lockFileAcceptingProcessingResults(ctx, store, event.FileId)
		if err != nil {
			return err
		}
		if !acceptsResults {
			return errProcessingSourceUnavailable
		}
		if err := requireAcceptingTranscodeAllocation(ctx, store, event); err != nil {
			return err
		}
		if event.EntityType == managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK {
			trackStateApplied, err = finalizeTrackTranscodeState(ctx, store, event)
			if err != nil {
				return err
			}
			if !trackStateApplied {
				return errTranscodeAllocationUnavailable
			}
		}
		return completeTranscodeDerivatives(ctx, store, event)
	})
	if err == nil {
		return trackStateApplied, true, nil
	}
	return false, false, h.handleAllocatedTranscodeOutputError(ctx, event, err)
}

func requireAcceptingTranscodeAllocation(
	ctx context.Context,
	store Store,
	event *managev1.TranscodeCompleteEvent,
) error {
	job, err := trackedTranscodeAllocationForCompletion(ctx, store, event, true)
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
	store Store,
	event *managev1.TranscodeCompleteEvent,
) error {
	if event.EventType == managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_VIDEO {
		if err := completeAssetDerivative(
			ctx,
			store,
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
			store,
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
		store,
		event.FileId,
		managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS,
		event.Outputs.GetHls(),
	)
}

func (h *Handlers) handleAllocatedTranscodeOutputError(
	ctx context.Context,
	event *managev1.TranscodeCompleteEvent,
	err error,
) error {
	if errors.Is(err, errProcessingSourceUnavailable) {
		if cleanupErr := h.finishDeletedFileTranscodeJob(ctx, event); cleanupErr != nil {
			return fmt.Errorf("cleanup transcode jobs rejected by file deletion: %w", cleanupErr)
		}
		return nil
	}
	if errors.Is(err, errTranscodeAllocationUnavailable) {
		if retireErr := h.retireStaleTranscodeOutputs(ctx, event); retireErr != nil {
			return fmt.Errorf("retire outputs rejected by transcode allocation: %w", retireErr)
		}
		return h.finishTranscodeJob(ctx, event)
	}
	return fmt.Errorf("complete allocated transcode outputs: %w", err)
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
