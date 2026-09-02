package transcode

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const (
	WaveformJobStatusQueued    = "WAVEFORM_JOB_STATUS_QUEUED"
	WaveformJobStatusCompleted = "WAVEFORM_JOB_STATUS_COMPLETED"
	WaveformJobStatusFailed    = "WAVEFORM_JOB_STATUS_FAILED"
	WaveformJobStatusCancelled = "WAVEFORM_JOB_STATUS_CANCELLED"
)

type WaveformCancelPublisher interface {
	PublishWaveformCancel(ctx context.Context, event *managev1.WaveformCancelEvent) error
}

// CancelTrackWaveformJobs marks active Track waveform jobs cancelled before
// publishing their cancellation commands.
func CancelTrackWaveformJobs(
	ctx context.Context,
	db *gorm.DB,
	publisher WaveformCancelPublisher,
	trackIDs []string,
	reason managev1.TranscodeCancelReason,
) error {
	if len(trackIDs) == 0 {
		return nil
	}

	var jobs []model.WaveformJob
	if err := db.WithContext(ctx).
		Where("entity_type = ? AND entity_id IN ? AND status = ?", managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK.String(), trackIDs, WaveformJobStatusQueued).
		Find(&jobs).Error; err != nil {
		return err
	}
	if len(jobs) == 0 {
		return nil
	}

	now := time.Now()
	errText := fmt.Sprintf("cancelled: %s", reason.String())
	jobEventIDs := make([]string, 0, len(jobs))
	for _, job := range jobs {
		jobEventIDs = append(jobEventIDs, job.EventID)
	}

	if err := db.WithContext(ctx).
		Model(&model.WaveformJob{}).
		Where("event_id IN ?", jobEventIDs).
		Updates(structured.Fields{
			"status":           WaveformJobStatusCancelled,
			"cancel_requested": true,
			"last_error":       errText,
			"completed_at":     now,
			"updated_at":       now,
		}).Error; err != nil {
		return err
	}

	if publisher == nil {
		return nil
	}

	for _, job := range jobs {
		if err := publisher.PublishWaveformCancel(ctx, &managev1.WaveformCancelEvent{
			EventId:     job.EventID,
			EntityType:  managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK,
			EntityId:    job.EntityID,
			FileId:      job.FileID,
			Reason:      reason,
			TimestampMs: time.Now().UnixMilli(),
		}); err != nil {
			return err
		}
	}

	return nil
}
