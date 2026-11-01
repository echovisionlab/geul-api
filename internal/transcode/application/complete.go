package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/failurecause"
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
func (h *Handlers) HandleTranscodeComplete(ctx context.Context, body []byte) (resultErr error) {
	event, err := parseTranscodeCompleteEvent(body)
	// The queue consumer logs the failure; the event names its object.
	defer func() {
		resultErr = failurecause.WithAttrs(resultErr, slog.String("event_id", event.GetEventId()), slog.String("file_id", event.GetFileId()),
			slog.String("entity_type", event.GetEntityType().String()), slog.String("entity_id", event.GetEntityId()))
	}()
	if err != nil {
		return err
	}
	logTranscodeComplete(ctx, event)

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
	err := h.transaction(ctx, func(store Store) error {
		acceptsResults, err := store.LockFileAcceptingResults(ctx, event.GetFileId())
		if err != nil {
			return err
		}
		if !acceptsResults {
			return errProcessingSourceUnavailable
		}
		job, err := trackedTranscodeJobByIdentity(ctx, store, event, true)
		if err != nil {
			return err
		}
		if job == nil || !transcodeJobAcceptsCompletion(job.Status) {
			return errTranscodeAllocationUnavailable
		}
		errText := "transcode failed"
		if event.Error != nil && strings.TrimSpace(*event.Error) != "" {
			errText = strings.TrimSpace(*event.Error)
		}
		failed, err := store.FailActiveTranscodeJob(ctx, job.EventID, errText, time.Now().UTC())
		if err != nil {
			return err
		}
		if !failed {
			return errTranscodeAllocationUnavailable
		}
		if event.GetEntityType() == managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK {
			ownerStateApplied, err = finalizeTrackTranscodeState(ctx, store, event)
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
	var accepts bool
	err := h.read(ctx, func(store Store) (err error) {
		accepts, err = store.FileAcceptsResults(ctx, fileID)
		return err
	})
	return accepts, err
}

func lockFileAcceptingProcessingResults(ctx context.Context, store Store, fileID string) (bool, error) {
	if strings.TrimSpace(fileID) == "" {
		return false, nil
	}
	return store.LockFileAcceptingResults(ctx, strings.TrimSpace(fileID))
}

func (h *Handlers) finishDeletedFileTranscodeJob(
	ctx context.Context,
	event *managev1.TranscodeCompleteEvent,
) error {
	if event == nil || strings.TrimSpace(event.GetFileId()) == "" || strings.TrimSpace(event.GetEventId()) == "" {
		return nil
	}
	return h.transaction(ctx, func(store Store) error {
		return store.DeleteTranscodeJob(ctx, event.GetEventId(), event.GetFileId())
	})
}

func (h *Handlers) retireStaleTranscodeOutputs(ctx context.Context, event *managev1.TranscodeCompleteEvent) error {
	if event == nil || event.Outputs == nil {
		return nil
	}
	return h.transaction(ctx, func(store Store) error {
		acceptsResults, err := lockFileAcceptingProcessingResults(ctx, store, event.GetFileId())
		if err != nil || !acceptsResults {
			return err
		}
		tracked, err := trackedTranscodeAllocationForCompletion(ctx, store, event, true)
		if err != nil {
			return err
		}
		if tracked == nil {
			slog.WarnContext(ctx, "Skipped stale transcode output retirement without matching allocation",
				"fileId", event.FileId,
				"entityType", event.EntityType.String(),
				"entityId", event.EntityId,
			)
			return nil
		}
		now := time.Now().UTC()
		if transcodeJobAcceptsCompletion(tracked.Status) {
			if err := store.CancelActiveTranscodeJob(ctx, tracked.EventID, "stale completion no longer owns the entity projection", now); err != nil {
				return err
			}
		}
		for _, target := range collectStaleTranscodeOutputTargets(event.Outputs) {
			if target.derivativeType == managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS {
				if err := retireStaleMediaGeneration(ctx, store, event.GetFileId(), event.Outputs.GetHls(), now); err != nil {
					return err
				}
				continue
			}
			if err := markStalePublicAssetDeletePending(ctx, store, event.GetFileId(), target.id, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func trackedTranscodeAllocationForCompletion(
	ctx context.Context,
	store Store,
	event *managev1.TranscodeCompleteEvent,
	lock bool,
) (*transcodestate.Job, error) {
	job, err := trackedTranscodeJobByIdentity(ctx, store, event, lock)
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

func trackedTranscodeJobByIdentity(
	ctx context.Context,
	store Store,
	event *managev1.TranscodeCompleteEvent,
	lock bool,
) (*transcodestate.Job, error) {
	var queueName string
	switch event.GetEventType() {
	case managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO:
		queueName = eventpkg.QueueTranscoderAudio
	case managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_VIDEO:
		queueName = eventpkg.QueueTranscoderVideo
	default:
		return nil, nil
	}
	return store.TranscodeJob(ctx, transcodestate.CompletionIdentity{
		EventID: event.GetEventId(), QueueName: queueName, EntityType: event.GetEntityType().String(),
		EntityID: event.GetEntityId(), FileID: event.GetFileId(),
	}, lock)
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
	store Store,
	fileID string,
	result *commonv1.MediaGenerationWriteResult,
	now time.Time,
) error {
	if result == nil {
		return fmt.Errorf("stale media generation result is required")
	}
	generation, found, err := store.LockMediaGeneration(ctx, strings.TrimSpace(result.GetGenerationId()))
	if err != nil || !found {
		return err
	}
	if generation.FileID != fileID || generation.Kind != "hls" {
		return fmt.Errorf("stale media generation does not belong to source file")
	}
	derivativeCount, err := store.GenerationDerivativeCount(ctx, generation.ID)
	if err != nil {
		return err
	}
	if derivativeCount != 0 || generation.Status == GenerationRetired {
		return nil
	}
	if generation.Status != GenerationAllocated {
		result = nil
	}
	return store.RetireMediaGeneration(ctx, generation.ID, result, now)
}

func markStalePublicAssetDeletePending(
	ctx context.Context,
	store Store,
	fileID string,
	assetID string,
	now time.Time,
) error {
	asset, found, err := store.LockAsset(ctx, assetID)
	if err != nil || !found {
		return err
	}
	if asset.SourceFileID == nil || *asset.SourceFileID != fileID {
		return fmt.Errorf("stale public asset does not belong to source file")
	}
	derivativeCount, err := store.AssetDerivativeCount(ctx, asset.ID)
	if err != nil {
		return err
	}
	bindingCount, err := store.AssetBindingCount(ctx, asset.ID)
	if err != nil {
		return err
	}
	if derivativeCount != 0 || bindingCount != 0 ||
		asset.Status == AssetDeletePending || asset.Status == AssetDeleted {
		return nil
	}
	return store.MarkAssetDeletePending(ctx, asset.ID, now)
}

func (h *Handlers) persistFileDurationSeconds(ctx context.Context, fileID string, durationSeconds int32) error {
	if durationSeconds < 0 {
		return nil
	}
	return h.transaction(ctx, func(store Store) error {
		return store.SetFileDuration(ctx, fileID, int(durationSeconds))
	})
}

func finalizeTrackTranscodeState(
	ctx context.Context,
	store Store,
	event *managev1.TranscodeCompleteEvent,
) (bool, error) {
	if event.EntityType != managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK {
		return false, nil
	}

	status, err := determineTrackAudioProcessingStatus(ctx, store, event.FileId)
	if err != nil {
		return false, err
	}

	var duration *int
	if event.Success && event.Outputs != nil && event.Outputs.DurationSeconds != nil {
		duration = new(int(*event.Outputs.DurationSeconds))
	}
	applied, err := store.UpdateTrackProcessing(ctx, event.EntityId, event.FileId, status, duration)
	if err != nil {
		return false, err
	}
	if !applied {
		slog.DebugContext(ctx, "Skipped stale track transcode completion", "trackId", event.EntityId, "fileId", event.FileId)
	}
	return applied, nil
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

	var sourceTarget *commonv1.MediaObjectTarget
	var found bool
	if err := h.read(ctx, func(store Store) (err error) {
		sourceTarget, found, err = store.WaveformSource(ctx, event.FileId)
		return err
	}); err != nil || !found {
		return err
	}
	fileID := strings.TrimSpace(event.FileId)
	var outputTarget *commonv1.AssetWriteTarget
	if err := h.transaction(ctx, func(store Store) (err error) {
		outputTarget, err = store.AllocateWaveformOutput(ctx, fileID)
		return err
	}); err != nil {
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
			return errors.Join(err, fmt.Errorf("mark waveform job %s publish failure: %w", job.EventId, markErr))
		}
		return err
	}

	return nil
}
