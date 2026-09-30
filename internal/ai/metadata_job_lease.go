package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/structured"
)

var errMetadataAIJobLeaseUnavailable = errors.New("metadata AI job is being processed by another delivery")

func (m *MetadataJobManager) claimJob(
	ctx context.Context,
	job *metadataJobRecord,
	startedAt time.Time,
) (bool, error) {
	query := m.db.WithContext(ctx).Model(&metadataJobRecord{})
	switch job.Status {
	case metadataAIJobStatusQueued:
		query = query.Where("id = ? AND status = ?", job.ID, metadataAIJobStatusQueued)
	case metadataAIJobStatusRunning:
		query = query.Where("id = ? AND status = ?", job.ID, metadataAIJobStatusRunning)
		if job.StartedAt == nil {
			query = query.Where("started_at IS NULL")
		} else {
			query = query.Where("started_at = ?", *job.StartedAt)
		}
	default:
		return false, nil
	}
	result := query.Updates(structured.Fields{
		"status":     metadataAIJobStatusRunning,
		"provider":   m.provider.ProviderName(),
		"model":      m.provider.ModelName(),
		"started_at": startedAt,
		"updated_at": startedAt,
	})
	if result.Error != nil {
		return false, fmt.Errorf("failed to mark metadata AI job running: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return false, nil
	}

	job.Status = metadataAIJobStatusRunning
	job.Provider = new(m.provider.ProviderName())
	job.Model = new(m.provider.ModelName())
	job.StartedAt = &startedAt
	job.UpdatedAt = startedAt
	return true, nil
}

func (m *MetadataJobManager) failJob(
	ctx context.Context,
	job *metadataJobRecord,
	startedAt time.Time,
	duration time.Duration,
	cause error,
) error {
	finishedAt := time.Now()
	durationMS := duration.Milliseconds()
	message := cause.Error()
	updates := structured.Fields{
		"status":       metadataAIJobStatusFailed,
		"error":        message,
		"duration_ms":  durationMS,
		"completed_at": finishedAt,
		"updated_at":   finishedAt,
	}
	persistenceCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), metadataAIPersistenceTimeout)
	defer cancel()
	result := m.db.WithContext(persistenceCtx).
		Model(&metadataJobRecord{}).
		Where("id = ? AND status = ? AND started_at = ?", job.ID, metadataAIJobStatusRunning, startedAt).
		Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("metadata AI job failed with %q and the failure state could not be stored: %w", message, result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("%w: %s", errMetadataAIJobLeaseUnavailable, job.ID)
	}

	job.Status = metadataAIJobStatusFailed
	job.Error = &message
	job.DurationMS = &durationMS
	job.CompletedAt = &finishedAt
	job.UpdatedAt = finishedAt

	slog.Warn("Metadata AI job failed",
		"job_id", job.ID,
		"target_type", job.TargetType,
		"target_id", job.TargetID,
		"duration_ms", durationMS,
		"error", message,
	)

	return nil
}

func (m *MetadataJobManager) completeJob(
	ctx context.Context,
	job *metadataJobRecord,
	startedAt time.Time,
	suggestion map[string]string,
	responseText string,
) error {
	completedAt := time.Now()
	durationMS := time.Since(startedAt).Milliseconds()
	suggestionJSON, err := json.Marshal(suggestion)
	if err != nil {
		return m.failJob(ctx, job, startedAt, time.Since(startedAt), err)
	}
	updates := structured.Fields{
		"status":        metadataAIJobStatusReady,
		"suggestion":    string(suggestionJSON),
		"response_text": responseText,
		"error":         nil,
		"duration_ms":   durationMS,
		"completed_at":  completedAt,
		"updated_at":    completedAt,
	}
	result := m.db.WithContext(ctx).
		Model(&metadataJobRecord{}).
		Where("id = ? AND status = ? AND started_at = ?", job.ID, metadataAIJobStatusRunning, startedAt).
		Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("failed to finalize metadata AI job: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("%w: %s", errMetadataAIJobLeaseUnavailable, job.ID)
	}

	job.Status = metadataAIJobStatusReady
	job.Suggestion = suggestion
	job.ResponseText = &responseText
	job.Error = nil
	job.DurationMS = &durationMS
	job.CompletedAt = &completedAt
	job.UpdatedAt = completedAt

	slog.Info("Metadata AI job completed",
		"job_id", job.ID,
		"target_type", job.TargetType,
		"target_id", job.TargetID,
		"duration_ms", durationMS,
		"requested_keys", job.RequestedKeys,
	)

	return nil
}
