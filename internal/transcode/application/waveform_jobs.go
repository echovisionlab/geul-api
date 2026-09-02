package application

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	transcodestate "github.com/echovisionlab/geul-api/internal/transcode"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

func (h *Handlers) upsertWaveformJob(ctx context.Context, event *managev1.WaveformGenerateEvent) error {
	if err := transcodestate.ValidateWaveformGenerateEvent(event); err != nil {
		return err
	}
	now := time.Now()
	record := model.WaveformJob{
		EventID:         event.EventId,
		EntityType:      event.EntityType.String(),
		EntityID:        event.EntityId,
		FileID:          event.FileId,
		Status:          transcodestate.WaveformJobStatusQueued,
		Progress:        0,
		CancelRequested: false,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	return h.db.WithContext(ctx).Save(&record).Error
}

func (h *Handlers) getWaveformJob(ctx context.Context, eventID string) (*model.WaveformJob, error) {
	var job model.WaveformJob
	if err := h.db.WithContext(ctx).Where("event_id = ?", eventID).First(&job).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &job, nil
}

func (h *Handlers) markWaveformJobCompleted(ctx context.Context, eventID string) error {
	return h.db.WithContext(ctx).
		Where("event_id = ?", eventID).
		Delete(&model.WaveformJob{}).Error
}

func (h *Handlers) finishDeletedFileWaveformJob(ctx context.Context, eventID, fileID string) error {
	eventID = strings.TrimSpace(eventID)
	fileID = strings.TrimSpace(fileID)
	if eventID == "" || fileID == "" {
		return nil
	}
	return h.db.WithContext(ctx).
		Where("event_id = ? AND file_id = ?", eventID, fileID).
		Delete(&model.WaveformJob{}).Error
}

func (h *Handlers) markWaveformJobFailed(ctx context.Context, eventID string, errText string) error {
	now := time.Now()
	return h.db.WithContext(ctx).
		Model(&model.WaveformJob{}).
		Where("event_id = ?", eventID).
		Updates(structured.Fields{
			"status":       transcodestate.WaveformJobStatusFailed,
			"last_error":   errText,
			"completed_at": now,
			"updated_at":   now,
		}).Error
}

func (h *Handlers) markWaveformJobCancelled(ctx context.Context, eventID string, errText string) error {
	now := time.Now()
	return h.db.WithContext(ctx).
		Model(&model.WaveformJob{}).
		Where("event_id = ?", eventID).
		Updates(structured.Fields{
			"status":           transcodestate.WaveformJobStatusCancelled,
			"cancel_requested": true,
			"last_error":       errText,
			"completed_at":     now,
			"updated_at":       now,
		}).Error
}

func (h *Handlers) shouldEnqueueWaveformJob(ctx context.Context, fileID string) (bool, error) {
	acceptsResults, err := h.fileAcceptsProcessingResults(ctx, fileID)
	if err != nil || !acceptsResults {
		return false, err
	}

	var derivativeCount int64
	if err := h.db.WithContext(ctx).
		Table("file_derivative").
		Where("file_id = ? AND type = ?", fileID, managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM.String()).
		Count(&derivativeCount).Error; err != nil {
		return false, err
	}
	if derivativeCount > 0 {
		return false, nil
	}

	var queuedCount int64
	if err := h.db.WithContext(ctx).
		Model(&model.WaveformJob{}).
		Where("file_id = ? AND status = ? AND cancel_requested = false", fileID, transcodestate.WaveformJobStatusQueued).
		Count(&queuedCount).Error; err != nil {
		return false, err
	}
	if queuedCount > 0 {
		return false, nil
	}

	failedCount, err := h.failedWaveformJobs(ctx, fileID)
	if err != nil {
		return false, err
	}

	return failedCount == 0, nil
}

func (h *Handlers) refreshTrackAudioProcessingStatus(ctx context.Context, trackID string, fileID string) error {
	status, err := h.determineTrackAudioProcessingStatus(ctx, fileID)
	if err != nil {
		return err
	}

	return h.db.WithContext(ctx).
		Model(&model.Track{}).
		Where("id = ? AND audio_original_file_id = ?", trackID, fileID).
		Update("processing_status", status).Error
}

func (h *Handlers) determineTrackAudioProcessingStatus(ctx context.Context, fileID string) (string, error) {
	return h.determineTrackAudioProcessingStatusWithDB(ctx, h.db, fileID)
}

func (h *Handlers) determineTrackAudioProcessingStatusWithDB(
	ctx context.Context,
	db *gorm.DB,
	fileID string,
) (string, error) {
	hasHLS, hasWaveform, hasSpectrogram, err := loadAudioDerivativePresenceWithDB(ctx, db, fileID)
	if err != nil {
		return "", err
	}
	if hasHLS && hasWaveform && hasSpectrogram {
		return managev1.TrackProcessingStatus_TRACK_PROCESSING_STATUS_COMPLETED.String(), nil
	}

	transcodeFailures, err := failedTranscodeJobsWithDB(ctx, db, fileID, eventpkg.QueueTranscoderAudio)
	if err != nil {
		return "", err
	}
	waveformFailures, err := failedWaveformJobsWithDB(ctx, db, fileID)
	if err != nil {
		return "", err
	}

	if ((!hasHLS || !hasSpectrogram) && transcodeFailures > 0) ||
		(!hasWaveform && waveformFailures > 0) {
		return managev1.TrackProcessingStatus_TRACK_PROCESSING_STATUS_FAILED.String(), nil
	}

	return managev1.TrackProcessingStatus_TRACK_PROCESSING_STATUS_PROCESSING.String(), nil
}

func loadAudioDerivativePresenceWithDB(
	ctx context.Context,
	db *gorm.DB,
	fileID string,
) (bool, bool, bool, error) {
	var derivativeTypes []string
	if err := db.WithContext(ctx).
		Table("file_derivative").
		Where("file_id = ? AND type IN ?", fileID, []string{
			managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String(),
			managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM.String(),
			managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_SPECTROGRAM.String(),
		}).
		Pluck("type", &derivativeTypes).Error; err != nil {
		return false, false, false, err
	}

	var hasHLS bool
	var hasWaveform bool
	var hasSpectrogram bool
	for _, derivativeType := range derivativeTypes {
		switch derivativeType {
		case managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String():
			hasHLS = true
		case managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM.String():
			hasWaveform = true
		case managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_SPECTROGRAM.String():
			hasSpectrogram = true
		}
	}

	return hasHLS, hasWaveform, hasSpectrogram, nil
}

func failedTranscodeJobsWithDB(ctx context.Context, db *gorm.DB, fileID string, queueName string) (int64, error) {
	var total int64
	if err := db.WithContext(ctx).
		Model(&model.TranscodeJob{}).
		Where("file_id = ? AND queue_name = ? AND status = ?", fileID, queueName, "TRANSCODE_JOB_STATUS_FAILED_TERMINAL").
		Count(&total).Error; err != nil {
		return 0, err
	}
	return total, nil
}

func (h *Handlers) failedWaveformJobs(ctx context.Context, fileID string) (int64, error) {
	return failedWaveformJobsWithDB(ctx, h.db, fileID)
}

func failedWaveformJobsWithDB(ctx context.Context, db *gorm.DB, fileID string) (int64, error) {
	var total int64
	if err := db.WithContext(ctx).
		Model(&model.WaveformJob{}).
		Where("file_id = ? AND status = ? AND cancel_requested = false", fileID, transcodestate.WaveformJobStatusFailed).
		Count(&total).Error; err != nil {
		return 0, err
	}
	return total, nil
}
