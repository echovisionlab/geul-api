package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (h *Handlers) HandleTranscodeProgress(ctx context.Context, body []byte) error {
	if h.transcodeJobs == nil {
		return nil
	}

	var event managev1.TranscodeProgressEvent
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
	tx *gorm.DB,
	lifecycle *mediaasset.Lifecycle,
	fileID string,
	derivativeType managev1.FileDerivativeType,
	expectedKind string,
	result *commonv1.AssetWriteResult,
) error {
	var previous struct {
		AssetID *string `gorm:"column:asset_id"`
	}
	if err := tx.Table("file_derivative").
		Select("asset_id").
		Where("file_id = ? AND type = ?", fileID, derivativeType.String()).
		Take(&previous).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	asset, err := lifecycle.CompletePublicAsset(ctx, result)
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
	if err := upsertFileDerivative(ctx, tx, model.FileDerivative{
		ID:      uuid.NewString(),
		FileID:  fileID,
		Type:    derivativeType,
		AssetID: &assetID,
	}); err != nil {
		return err
	}
	return retireReplacedDerivativeAsset(ctx, tx, lifecycle, previous.AssetID, assetID)
}

func retireReplacedDerivativeAsset(
	ctx context.Context,
	tx *gorm.DB,
	lifecycle *mediaasset.Lifecycle,
	previousAssetID *string,
	currentAssetID string,
) error {
	if previousAssetID == nil || strings.TrimSpace(*previousAssetID) == "" || strings.TrimSpace(*previousAssetID) == currentAssetID {
		return nil
	}
	var references int64
	if err := tx.Table("file_derivative").Where("asset_id = ?", *previousAssetID).Count(&references).Error; err != nil {
		return err
	}
	if references > 0 {
		return nil
	}
	if err := tx.Table("public_asset_binding").Where("asset_id = ?", *previousAssetID).Count(&references).Error; err != nil {
		return err
	}
	if references > 0 {
		return nil
	}
	return lifecycle.RequestPublicAssetDeletion(ctx, *previousAssetID)
}

func completeGenerationDerivative(
	ctx context.Context,
	tx *gorm.DB,
	lifecycle *mediaasset.Lifecycle,
	fileID string,
	derivativeType managev1.FileDerivativeType,
	result *commonv1.MediaGenerationWriteResult,
) error {
	generation, err := lifecycle.CompleteMediaGeneration(ctx, result)
	if err != nil {
		return err
	}
	if strings.TrimSpace(generation.FileID) != strings.TrimSpace(fileID) {
		return fmt.Errorf("media generation %s is not allocated for source file %s", generation.ID, fileID)
	}
	generationID := generation.ID
	return upsertFileDerivative(ctx, tx, model.FileDerivative{
		ID:                uuid.NewString(),
		FileID:            fileID,
		Type:              derivativeType,
		MediaGenerationID: &generationID,
	})
}

func upsertFileDerivative(ctx context.Context, db *gorm.DB, derivative model.FileDerivative) error {
	return db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "file_id"}, {Name: "type"}},
		DoUpdates: clause.Assignments(structured.Fields{
			"asset_id":            derivative.AssetID,
			"media_generation_id": derivative.MediaGenerationID,
		}),
	}).Create(&derivative).Error
}

func (h *Handlers) removeFileDerivative(
	ctx context.Context,
	fileID string,
	derivativeType managev1.FileDerivativeType,
) error {
	return h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing struct {
			AssetID *string `gorm:"column:asset_id"`
		}
		result := tx.Table("file_derivative").
			Select("asset_id").
			Where("file_id = ? AND type = ?", fileID, derivativeType.String()).
			First(&existing)
		if result.Error != nil {
			if result.Error == gorm.ErrRecordNotFound {
				return nil
			}
			return result.Error
		}
		if existing.AssetID == nil || strings.TrimSpace(*existing.AssetID) == "" {
			return fmt.Errorf("%s derivative for file %s has no asset ID", derivativeType.String(), fileID)
		}
		if err := tx.Table("file_derivative").
			Where("file_id = ? AND type = ?", fileID, derivativeType.String()).
			Delete(nil).Error; err != nil {
			return err
		}
		return mediaasset.NewLifecycle(tx, "").RequestPublicAssetDeletion(ctx, *existing.AssetID)
	})
}
