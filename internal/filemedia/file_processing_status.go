package filemedia

import (
	"context"
	"strings"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/transcode"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

type fileProcessingSnapshot struct {
	Status     commonv1.MediaProcessingStatus
	Percentage *int32
}

type failedTranscodeJobRow struct {
	FileID string `gorm:"column:file_id"`
	Count  int64  `gorm:"column:failure_count"`
}

type failedWaveformJobRow struct {
	FileID string `gorm:"column:file_id"`
	Count  int64  `gorm:"column:failure_count"`
}

func resolveFileProcessingSnapshot(
	mimeType string,
	hasHLS bool,
	hasWaveform bool,
	hasSpectrogram bool,
	transcodeFailures int64,
	waveformFailures int64,
) fileProcessingSnapshot {
	normalizedMime := normalizeMimeType(mimeType)
	if strings.HasPrefix(normalizedMime, "video/") {
		return resolveVideoProcessingSnapshot(hasHLS, transcodeFailures)
	}
	if strings.HasPrefix(normalizedMime, "audio/") {
		return resolveAudioProcessingSnapshot(
			hasHLS, hasWaveform, hasSpectrogram, transcodeFailures, waveformFailures,
		)
	}
	return fileProcessingSnapshot{
		Status: commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_READY,
	}
}

func resolveVideoProcessingSnapshot(hasHLS bool, transcodeFailures int64) fileProcessingSnapshot {
	if hasHLS {
		return fileProcessingSnapshot{Status: commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_READY}
	}
	if transcodeFailures > 0 {
		return fileProcessingSnapshot{Status: commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_FAILED}
	}
	return fileProcessingSnapshot{
		Status: commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_PROCESSING, Percentage: new(int32(0)),
	}
}

func resolveAudioProcessingSnapshot(
	hasHLS bool,
	hasWaveform bool,
	hasSpectrogram bool,
	transcodeFailures int64,
	waveformFailures int64,
) fileProcessingSnapshot {
	completed := countCompletedAudioDerivatives(hasHLS, hasWaveform, hasSpectrogram)
	if completed == 3 {
		return fileProcessingSnapshot{Status: commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_READY}
	}
	transcodeFailed := (!hasHLS || !hasSpectrogram) && transcodeFailures > 0
	waveformFailed := !hasWaveform && waveformFailures > 0
	if transcodeFailed || waveformFailed {
		return fileProcessingSnapshot{Status: commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_FAILED}
	}
	percentage := int32(0)
	if completed > 0 {
		percentage = (completed*100 + 1) / 3
	}
	return fileProcessingSnapshot{
		Status: commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_PROCESSING, Percentage: new(percentage),
	}
}

func countCompletedAudioDerivatives(hasHLS, hasWaveform, hasSpectrogram bool) int32 {
	var completed int32
	for _, ready := range []bool{hasHLS, hasWaveform, hasSpectrogram} {
		if ready {
			completed++
		}
	}
	return completed
}

func (s *FileService) populateFileProcessingStatus(
	ctx context.Context,
	responses map[string]*commonv1.MediaDelivery,
) error {
	if len(responses) == 0 {
		return nil
	}

	audioIDs := make([]string, 0, len(responses))
	videoIDs := make([]string, 0, len(responses))
	for fileID, response := range responses {
		if response == nil {
			continue
		}
		switch {
		case strings.HasPrefix(normalizeMimeType(response.GetMimeType()), "audio/"):
			audioIDs = append(audioIDs, fileID)
		case strings.HasPrefix(normalizeMimeType(response.GetMimeType()), "video/"):
			videoIDs = append(videoIDs, fileID)
		}
	}

	audioTranscodeFailures, err := s.loadFailedTranscodeJobsByFileIDs(ctx, audioIDs, eventpkg.QueueTranscoderAudio)
	if err != nil {
		return err
	}
	videoTranscodeFailures, err := s.loadFailedTranscodeJobsByFileIDs(ctx, videoIDs, eventpkg.QueueTranscoderVideo)
	if err != nil {
		return err
	}
	waveformFailures, err := s.loadFailedWaveformJobsByFileIDs(ctx, audioIDs)
	if err != nil {
		return err
	}

	for fileID, response := range responses {
		if response == nil {
			continue
		}
		snapshot := resolveFileProcessingSnapshot(
			response.GetMimeType(),
			response.GetPlayback() != nil && response.GetPlayback().GetUrl() != "",
			response.GetWaveform() != nil && response.GetWaveform().GetUrl() != "",
			response.GetSpectrogram() != nil && response.GetSpectrogram().GetUrl() != "",
			audioTranscodeFailures[fileID]+videoTranscodeFailures[fileID],
			waveformFailures[fileID],
		)
		response.ProcessingStatus = snapshot.Status
		response.ProcessingPercentage = snapshot.Percentage
	}

	return nil
}

func (s *FileService) loadFailedTranscodeJobsByFileIDs(
	ctx context.Context,
	fileIDs []string,
	queueName string,
) (map[string]int64, error) {
	result := make(map[string]int64, len(fileIDs))
	if len(fileIDs) == 0 {
		return result, nil
	}

	var rows []failedTranscodeJobRow
	if err := s.db.WithContext(ctx).
		Model(&model.TranscodeJob{}).
		Select("file_id, COUNT(*) AS failure_count").
		Where("file_id IN ? AND queue_name = ? AND status = ?", fileIDs, queueName, "TRANSCODE_JOB_STATUS_FAILED_TERMINAL").
		Group("file_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	for _, row := range rows {
		result[row.FileID] = row.Count
	}
	return result, nil
}

func (s *FileService) loadFailedWaveformJobsByFileIDs(
	ctx context.Context,
	fileIDs []string,
) (map[string]int64, error) {
	result := make(map[string]int64, len(fileIDs))
	if len(fileIDs) == 0 {
		return result, nil
	}

	var rows []failedWaveformJobRow
	if err := s.db.WithContext(ctx).
		Model(&model.WaveformJob{}).
		Select("file_id, COUNT(*) AS failure_count").
		Where("file_id IN ? AND status = ? AND cancel_requested = false", fileIDs, transcode.WaveformJobStatusFailed).
		Group("file_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	for _, row := range rows {
		result[row.FileID] = row.Count
	}
	return result, nil
}
