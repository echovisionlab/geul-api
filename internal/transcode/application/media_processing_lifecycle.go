package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	transcodestate "github.com/echovisionlab/geul-api/internal/transcode"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

type mediaProcessingDerivative struct {
	AssetID           *string
	MediaGenerationID *string
}

type mediaProcessingSnapshot struct {
	File                model.File
	Derivatives         map[string]mediaProcessingDerivative
	HLSProgress         int32
	SpectrogramProgress int32
	WaveformProgress    int32
	TranscodeFailed     bool
	WaveformFailed      bool
}

type mediaProcessingPublishOptions struct {
	EntityType    managev1.TranscodeEntityType
	EntityID      string
	FileID        string
	CorrelationID string
	Sequence      int64
	TimestampMs   int64
	Error         string
}

func (h *Handlers) publishMediaProcessingLifecycle(
	ctx context.Context,
	options mediaProcessingPublishOptions,
) error {
	if h.publisher == nil || strings.TrimSpace(options.FileID) == "" {
		return nil
	}

	identity, ok, err := h.resolveMediaProcessingLifecycleIdentity(
		ctx,
		options.EntityType,
		options.EntityID,
	)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	snapshot, err := h.loadMediaProcessingSnapshot(ctx, options.FileID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}

	event, err := h.planMediaProcessingLifecycleEvent(options, identity, snapshot)
	if err != nil {
		return err
	}

	if err := h.publisher.PublishMediaProcessingLifecycle(ctx, event); err != nil {
		slog.Warn("Failed to publish media processing lifecycle",
			"fileId", options.FileID,
			"status", event.Status.String(),
			"error", err,
		)
		return err
	}

	return nil
}

func (h *Handlers) planMediaProcessingLifecycleEvent(
	options mediaProcessingPublishOptions,
	identity mediaProcessingLifecycleIdentity,
	snapshot mediaProcessingSnapshot,
) (*managev1.MediaProcessingLifecycleEvent, error) {
	status := snapshot.lifecycleStatus()
	if status != commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_READY &&
		strings.TrimSpace(options.Error) != "" {
		status = commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_FAILED
	}

	var percentage *int32
	var outputs *managev1.MediaProcessingLifecycleOutputs
	var eventError *string
	switch status {
	case commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_PROCESSING:
		percentage = new(snapshot.aggregateProgress())
	case commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_READY:
		var err error
		outputs, err = h.buildMediaProcessingLifecycleOutputs(snapshot)
		if err != nil {
			return nil, err
		}
	case commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_FAILED:
		eventError = optionalString(options.Error)
	default:
		return nil, fmt.Errorf("invalid media processing lifecycle status for file %s", options.FileID)
	}

	timestampMs := options.TimestampMs
	if timestampMs == 0 {
		timestampMs = time.Now().UnixMilli()
	}

	event := &managev1.MediaProcessingLifecycleEvent{
		CorrelationId:  options.CorrelationID,
		EntityType:     options.EntityType,
		EntityId:       options.EntityID,
		FileId:         options.FileID,
		Status:         status,
		Percentage:     percentage,
		Outputs:        outputs,
		Error:          eventError,
		SequenceNumber: options.Sequence,
		TimestampMs:    timestampMs,
		SlotId:         optionalString(stringValue(snapshot.File.IngestSlotID)),
		AttemptId:      optionalString(stringValue(snapshot.File.IngestAttemptID)),
		TrackId:        optionalString(identity.TrackID),
		ReleaseId:      optionalString(identity.ReleaseID),
	}

	return event, nil
}

type mediaProcessingLifecycleIdentity struct {
	TrackID   string
	ReleaseID string
}

func (h *Handlers) resolveMediaProcessingLifecycleIdentity(
	ctx context.Context,
	entityType managev1.TranscodeEntityType,
	entityID string,
) (mediaProcessingLifecycleIdentity, bool, error) {
	switch entityType {
	case managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_FILE:
		if strings.TrimSpace(entityID) == "" {
			return mediaProcessingLifecycleIdentity{}, false, nil
		}
		return mediaProcessingLifecycleIdentity{}, true, nil
	case managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST:
		return mediaProcessingLifecycleIdentity{}, true, nil
	case managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE:
		return mediaProcessingLifecycleIdentity{}, true, nil
	case managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_WORK:
		return mediaProcessingLifecycleIdentity{}, true, nil
	case managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK:
		var track model.Track
		if err := h.db.WithContext(ctx).
			Select("id", "release_id").
			Where("id = ?", entityID).
			Take(&track).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return mediaProcessingLifecycleIdentity{}, false, nil
			}
			return mediaProcessingLifecycleIdentity{}, false, err
		}
		return mediaProcessingLifecycleIdentity{
			TrackID:   track.ID,
			ReleaseID: track.ReleaseID,
		}, true, nil
	default:
		return mediaProcessingLifecycleIdentity{}, false, nil
	}
}

func (h *Handlers) loadMediaProcessingSnapshot(
	ctx context.Context,
	fileID string,
) (mediaProcessingSnapshot, error) {
	var file model.File
	if err := h.db.WithContext(ctx).Where("id = ?", fileID).Take(&file).Error; err != nil {
		return mediaProcessingSnapshot{}, err
	}

	snapshot := mediaProcessingSnapshot{
		File:        file,
		Derivatives: make(map[string]mediaProcessingDerivative),
	}

	var derivatives []struct {
		Type              string  `gorm:"column:type"`
		AssetID           *string `gorm:"column:asset_id"`
		MediaGenerationID *string `gorm:"column:media_generation_id"`
	}
	if err := h.db.WithContext(ctx).
		Table("file_derivative AS fd").
		Select("fd.type", "fd.asset_id", "fd.media_generation_id").
		Joins("LEFT JOIN public_asset AS pa ON pa.id = fd.asset_id").
		Joins("LEFT JOIN media_generation AS mg ON mg.id = fd.media_generation_id").
		Where("fd.file_id = ?", fileID).
		Where(`(
			(fd.asset_id IS NOT NULL AND pa.status = ?)
			OR (fd.media_generation_id IS NOT NULL AND mg.status = ?)
		)`, model.PublicAssetStatusReady, model.MediaGenerationStatusReady).
		Find(&derivatives).Error; err != nil {
		return mediaProcessingSnapshot{}, err
	}
	for _, derivative := range derivatives {
		snapshot.Derivatives[derivative.Type] = mediaProcessingDerivative{
			AssetID:           derivative.AssetID,
			MediaGenerationID: derivative.MediaGenerationID,
		}
	}

	var transcodeJobs []model.TranscodeJob
	if err := h.db.WithContext(ctx).
		Where("file_id = ?", fileID).
		Find(&transcodeJobs).Error; err != nil {
		return mediaProcessingSnapshot{}, err
	}
	for _, job := range transcodeJobs {
		if snapshot.isAudio() && job.QueueName != eventpkg.QueueTranscoderAudio {
			continue
		}
		if snapshot.isVideo() && job.QueueName != eventpkg.QueueTranscoderVideo {
			continue
		}
		if job.Status == transcodestate.StatusCancelled ||
			job.Status == transcodestate.StatusCompleted {
			continue
		}
		if job.Status == transcodestate.StatusFailedTerminal {
			snapshot.TranscodeFailed = true
		}
		snapshot.HLSProgress = maxInt32(snapshot.HLSProgress, int32(job.HLSProgress))
		snapshot.SpectrogramProgress = maxInt32(
			snapshot.SpectrogramProgress,
			int32(job.SpectrogramProgress),
		)
	}

	var waveformJobs []model.WaveformJob
	if err := h.db.WithContext(ctx).
		Where("file_id = ?", fileID).
		Find(&waveformJobs).Error; err != nil {
		return mediaProcessingSnapshot{}, err
	}
	for _, job := range waveformJobs {
		if job.Status == transcodestate.WaveformJobStatusCancelled ||
			job.Status == transcodestate.WaveformJobStatusCompleted {
			continue
		}
		if job.Status == transcodestate.WaveformJobStatusFailed {
			snapshot.WaveformFailed = true
		}
		snapshot.WaveformProgress = maxInt32(snapshot.WaveformProgress, int32(job.Progress))
	}

	if snapshot.hasDerivative(managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS) {
		snapshot.HLSProgress = 100
	}
	if snapshot.hasDerivative(managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_SPECTROGRAM) {
		snapshot.SpectrogramProgress = 100
	}
	if snapshot.hasDerivative(managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM) {
		snapshot.WaveformProgress = 100
	}

	return snapshot, nil
}

func (s mediaProcessingSnapshot) isAudio() bool {
	return strings.HasPrefix(normalizeWorkerMimeType(s.File.MimeType), "audio/")
}

func (s mediaProcessingSnapshot) isVideo() bool {
	return strings.HasPrefix(normalizeWorkerMimeType(s.File.MimeType), "video/")
}

func (s mediaProcessingSnapshot) hasDerivative(
	derivativeType managev1.FileDerivativeType,
) bool {
	_, ok := s.Derivatives[derivativeType.String()]
	return ok
}

func (s mediaProcessingSnapshot) completedRequiredDerivatives() (int32, int32) {
	if s.isVideo() {
		completed := int32(0)
		if s.hasDerivative(managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS) {
			completed += 1
		}
		if s.hasDerivative(managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_THUMBNAIL) {
			completed += 1
		}
		return completed, 2
	}
	if s.isAudio() {
		completed := int32(0)
		if s.hasDerivative(managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS) {
			completed += 1
		}
		if s.hasDerivative(managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_SPECTROGRAM) {
			completed += 1
		}
		if s.hasDerivative(managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM) {
			completed += 1
		}
		return completed, 3
	}
	return 0, 0
}

func (s mediaProcessingSnapshot) lifecycleStatus() commonv1.MediaProcessingStatus {
	completedDerivatives, requiredDerivatives := s.completedRequiredDerivatives()
	if requiredDerivatives > 0 &&
		completedDerivatives >= requiredDerivatives &&
		s.File.DurationSeconds != nil {
		return commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_READY
	}
	if s.TranscodeFailed || s.WaveformFailed {
		return commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_FAILED
	}
	return commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_PROCESSING
}

func (s mediaProcessingSnapshot) aggregateProgress() int32 {
	if s.isVideo() {
		return clampInt32(s.HLSProgress)
	}
	if s.isAudio() {
		return clampInt32((s.HLSProgress + s.SpectrogramProgress + s.WaveformProgress + 1) / 3)
	}
	return 0
}

func (h *Handlers) buildMediaProcessingLifecycleOutputs(
	snapshot mediaProcessingSnapshot,
) (*managev1.MediaProcessingLifecycleOutputs, error) {
	outputs := &managev1.MediaProcessingLifecycleOutputs{}

	if derivative, ok := snapshot.Derivatives[managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_SPECTROGRAM.String()]; ok {
		outputs.SpectrogramAssetId = derivativeOutputID(derivative.AssetID)
	}
	if derivative, ok := snapshot.Derivatives[managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_THUMBNAIL.String()]; ok {
		outputs.ThumbnailAssetId = derivativeOutputID(derivative.AssetID)
	}
	if derivative, ok := snapshot.Derivatives[managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String()]; ok {
		outputs.HlsGenerationId = derivativeOutputID(derivative.MediaGenerationID)
	}
	if derivative, ok := snapshot.Derivatives[managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM.String()]; ok {
		outputs.WaveformAssetId = derivativeOutputID(derivative.AssetID)
	}
	if snapshot.File.DurationSeconds != nil {
		duration := int32(*snapshot.File.DurationSeconds)
		outputs.DurationSeconds = &duration
	}
	if err := validateReadyMediaProcessingOutputs(snapshot, outputs); err != nil {
		return nil, err
	}
	return outputs, nil
}

func normalizeWorkerMimeType(mimeType string) string {
	return strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0]))
}

func validateReadyMediaProcessingOutputs(
	snapshot mediaProcessingSnapshot,
	outputs *managev1.MediaProcessingLifecycleOutputs,
) error {
	if outputs == nil {
		return fmt.Errorf("ready media processing lifecycle for file %s has no outputs", snapshot.File.ID)
	}
	if outputs.DurationSeconds == nil {
		return fmt.Errorf("ready media processing lifecycle for file %s has no duration_seconds", snapshot.File.ID)
	}
	if snapshot.isAudio() {
		if outputs.HlsGenerationId == nil || outputs.SpectrogramAssetId == nil || outputs.WaveformAssetId == nil {
			return fmt.Errorf("ready audio lifecycle for file %s is missing required output ids", snapshot.File.ID)
		}
		return nil
	}
	if snapshot.isVideo() {
		if outputs.HlsGenerationId == nil || outputs.ThumbnailAssetId == nil {
			return fmt.Errorf("ready video lifecycle for file %s is missing required output ids", snapshot.File.ID)
		}
		return nil
	}
	return fmt.Errorf("ready media processing lifecycle for unsupported MIME type %q on file %s", snapshot.File.MimeType, snapshot.File.ID)
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func optionalString(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func derivativeOutputID(value *string) *string {
	if value == nil {
		return nil
	}
	return optionalString(*value)
}

func clampInt32(value int32) int32 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func maxInt32(a int32, b int32) int32 {
	if a > b {
		return a
	}
	return b
}
