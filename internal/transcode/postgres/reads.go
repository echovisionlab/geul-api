package transcodepostgres

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/transcode"
)

// ActiveJobFileIDs returns the Files among fileIDs that have a queued or
// processing transcode job, whose output prefix a cleanup must keep.
func ActiveJobFileIDs(ctx context.Context, db *gorm.DB, fileIDs []string) (map[string]struct{}, error) {
	result := make(map[string]struct{})
	if len(fileIDs) == 0 {
		return result, nil
	}
	var ids []string
	if err := db.WithContext(ctx).Model(&model.TranscodeJob{}).
		Where("file_id IN ? AND status IN ?", fileIDs, transcode.ActiveStatuses()).
		Pluck("file_id", &ids).Error; err != nil {
		return nil, err
	}
	for _, id := range ids {
		result[id] = struct{}{}
	}
	return result, nil
}

// PendingResultCounts counts the transcode and waveform jobs of fileID whose
// result may still arrive, including cancelled jobs a worker has not
// acknowledged yet. A File with pending results keeps its storage.
func PendingResultCounts(ctx context.Context, db *gorm.DB, fileID string) (transcodes, waveforms int64, err error) {
	if err := db.WithContext(ctx).Model(&model.TranscodeJob{}).
		Where("file_id = ? AND status IN ?", fileID, []string{transcode.StatusQueued, transcode.StatusProcessing, transcode.StatusCancelled}).
		Count(&transcodes).Error; err != nil {
		return 0, 0, err
	}
	if err := db.WithContext(ctx).Model(&model.WaveformJob{}).
		Where("file_id = ? AND status IN ?", fileID, []string{transcode.WaveformJobStatusQueued, transcode.WaveformJobStatusCancelled}).
		Count(&waveforms).Error; err != nil {
		return 0, 0, err
	}
	return transcodes, waveforms, nil
}

type failedJobCount struct {
	FileID string `gorm:"column:file_id"`
	Count  int64  `gorm:"column:failure_count"`
}

// FailedTranscodeCounts counts the terminally failed transcode jobs of each
// File in fileIDs on queueName.
func FailedTranscodeCounts(ctx context.Context, db *gorm.DB, fileIDs []string, queueName string) (map[string]int64, error) {
	return failedJobCounts(db.WithContext(ctx).Model(&model.TranscodeJob{}).
		Where("file_id IN ? AND queue_name = ? AND status = ?", fileIDs, queueName, transcode.StatusFailedTerminal), fileIDs)
}

// FailedWaveformCounts counts the failed waveform jobs of each File in
// fileIDs that no one cancelled.
func FailedWaveformCounts(ctx context.Context, db *gorm.DB, fileIDs []string) (map[string]int64, error) {
	return failedJobCounts(db.WithContext(ctx).Model(&model.WaveformJob{}).
		Where("file_id IN ? AND status = ? AND cancel_requested = false", fileIDs, transcode.WaveformJobStatusFailed), fileIDs)
}

func failedJobCounts(query *gorm.DB, fileIDs []string) (map[string]int64, error) {
	result := make(map[string]int64, len(fileIDs))
	if len(fileIDs) == 0 {
		return result, nil
	}
	var rows []failedJobCount
	if err := query.Select("file_id, COUNT(*) AS failure_count").Group("file_id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.FileID] = row.Count
	}
	return result, nil
}
