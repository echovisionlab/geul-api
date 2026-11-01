package application

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (h *Handlers) HandleTranscodeProgress(ctx context.Context, body []byte) (resultErr error) {
	if h.transcodeJobs == nil {
		return nil
	}

	var event managev1.TranscodeProgressEvent
	// The queue consumer logs the failure; the event names its object.
	defer func() {
		resultErr = failurecause.WithAttrs(resultErr, slog.String("event_id", event.GetEventId()), slog.String("file_id", event.GetFileId()),
			slog.String("entity_type", event.GetEntityType().String()), slog.String("entity_id", event.GetEntityId()))
	}()
	if err := proto.Unmarshal(body, &event); err != nil {
		return fmt.Errorf("failed to unmarshal transcode progress event: %w", err)
	}

	if err := h.transcodeJobs.HandleTranscodeProgress(ctx, &event); err != nil {
		return fmt.Errorf("update transcode progress state: %w", err)
	}

	if err := h.publishMediaProcessingLifecycle(ctx, mediaProcessingPublishOptions{
		EntityType:    event.EntityType,
		EntityID:      event.EntityId,
		FileID:        event.FileId,
		CorrelationID: event.EventId,
		Sequence:      event.SequenceNumber,
		TimestampMs:   event.TimestampMs,
	}); err != nil {
		return fmt.Errorf("publish transcode progress lifecycle: %w", err)
	}

	return nil
}

// upsertFileDerivative creates or updates a file derivative record.
// Uses ON CONFLICT to handle the UNIQUE(file_id, type) constraint.
func validateTranscodeCompletionOutputs(event *managev1.TranscodeCompleteEvent) error {
	if event == nil || event.Outputs == nil {
		return fmt.Errorf("transcode completion outputs are required")
	}
	if event.Outputs.GetHls() == nil {
		return fmt.Errorf("transcode completion HLS result is required")
	}
	if len(event.Outputs.GetHls().GetManifestSha256()) != 32 ||
		event.Outputs.GetHls().GetObjectCount() <= 0 || event.Outputs.GetHls().GetTotalSize() <= 0 {
		return fmt.Errorf("transcode completion HLS metadata is invalid")
	}
	validateAsset := func(result *commonv1.AssetWriteResult) error {
		if result == nil || result.GetFileSize() <= 0 || len(result.GetSha256()) != 32 {
			return fmt.Errorf("transcode completion asset metadata is invalid")
		}
		return nil
	}
	switch event.EventType {
	case managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO:
		if err := validateAsset(event.Outputs.GetSpectrogram()); err != nil {
			return fmt.Errorf("audio spectrogram: %w", err)
		}
	case managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_VIDEO:
		if err := validateAsset(event.Outputs.GetThumbnail()); err != nil {
			return fmt.Errorf("video thumbnail: %w", err)
		}
	default:
		return fmt.Errorf("transcode completion event_type is invalid")
	}
	return nil
}

func completeAssetDerivative(
	ctx context.Context,
	store Store,
	fileID string,
	derivativeType managev1.FileDerivativeType,
	expectedKind string,
	result *commonv1.AssetWriteResult,
) error {
	previousAssetID, _, err := store.DerivativeAsset(ctx, fileID, derivativeType.String())
	if err != nil {
		return err
	}
	asset, err := store.CompleteAsset(ctx, result)
	if err != nil {
		return err
	}
	if asset.SourceFileID == nil || strings.TrimSpace(*asset.SourceFileID) != strings.TrimSpace(fileID) {
		return fmt.Errorf("asset %s is not allocated for source file %s", asset.ID, fileID)
	}
	if asset.Kind != expectedKind {
		return fmt.Errorf("asset %s kind %s does not match %s", asset.ID, asset.Kind, expectedKind)
	}
	assetID := asset.ID
	if err := store.UpsertDerivative(ctx, Derivative{
		ID:      uuid.NewString(),
		FileID:  fileID,
		Type:    derivativeType.String(),
		AssetID: &assetID,
	}); err != nil {
		return err
	}
	return retireReplacedDerivativeAsset(ctx, store, previousAssetID, assetID)
}

func retireReplacedDerivativeAsset(
	ctx context.Context,
	store Store,
	previousAssetID *string,
	currentAssetID string,
) error {
	if previousAssetID == nil || strings.TrimSpace(*previousAssetID) == "" || strings.TrimSpace(*previousAssetID) == currentAssetID {
		return nil
	}
	references, err := store.AssetDerivativeCount(ctx, *previousAssetID)
	if err != nil || references > 0 {
		return err
	}
	references, err = store.AssetBindingCount(ctx, *previousAssetID)
	if err != nil || references > 0 {
		return err
	}
	return store.RequestAssetDeletion(ctx, *previousAssetID)
}

func completeGenerationDerivative(
	ctx context.Context,
	store Store,
	fileID string,
	derivativeType managev1.FileDerivativeType,
	result *commonv1.MediaGenerationWriteResult,
) error {
	generation, err := store.CompleteMediaGeneration(ctx, result)
	if err != nil {
		return err
	}
	if strings.TrimSpace(generation.FileID) != strings.TrimSpace(fileID) {
		return fmt.Errorf("media generation %s is not allocated for source file %s", generation.ID, fileID)
	}
	generationID := generation.ID
	return store.UpsertDerivative(ctx, Derivative{
		ID:                uuid.NewString(),
		FileID:            fileID,
		Type:              derivativeType.String(),
		MediaGenerationID: &generationID,
	})
}

func (h *Handlers) removeFileDerivative(
	ctx context.Context,
	fileID string,
	derivativeType managev1.FileDerivativeType,
) error {
	return h.transaction(ctx, func(store Store) error {
		assetID, found, err := store.DerivativeAsset(ctx, fileID, derivativeType.String())
		if err != nil || !found {
			return err
		}
		if assetID == nil || strings.TrimSpace(*assetID) == "" {
			return fmt.Errorf("%s derivative for file %s has no asset ID", derivativeType.String(), fileID)
		}
		if err := store.DeleteDerivative(ctx, fileID, derivativeType.String()); err != nil {
			return err
		}
		return store.RequestAssetDeletion(ctx, *assetID)
	})
}
