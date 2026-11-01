package ai

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/failurecause"
)

var errMetadataAIJobLeaseUnavailable = errors.New("metadata AI job is being processed by another delivery")

func (m *MetadataJobManager) claimJob(
	ctx context.Context,
	job *Job,
	startedAt time.Time,
) (bool, error) {
	if job.Status != JobQueued && job.Status != JobRunning {
		return false, nil
	}
	claimed, err := m.runJobUpdate(ctx, func(ctx context.Context, store Store) (bool, error) {
		return store.ClaimJob(ctx, *job, m.provider.ProviderName(), m.provider.ModelName(), startedAt)
	})
	if err != nil {
		return false, fmt.Errorf("failed to mark metadata AI job running: %w", err)
	}
	if !claimed {
		return false, nil
	}

	job.Status = JobRunning
	job.Provider = new(m.provider.ProviderName())
	job.Model = new(m.provider.ModelName())
	job.StartedAt = &startedAt
	job.UpdatedAt = startedAt
	return true, nil
}

func (m *MetadataJobManager) failJob(
	ctx context.Context,
	job *Job,
	startedAt time.Time,
	duration time.Duration,
	cause error,
) error {
	level := slog.LevelError
	if errors.Is(cause, context.Canceled) {
		level = slog.LevelWarn
	}
	slog.Log(ctx, level, "Metadata AI job failed", "domain", "ai", "event", "ai.metadata.job.failed", "outcome", "failed", failurecause.Attr(cause, job.Context, job.Prompt), "job_id", job.ID, "target_type", job.TargetType, "target_id", job.TargetID, "requested_keys", job.RequestedKeys, "duration_ms", duration.Milliseconds())
	finishedAt := time.Now()
	durationMS := duration.Milliseconds()
	message := cause.Error()
	persistenceCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), metadataAIPersistenceTimeout)
	defer cancel()
	finished, err := m.runJobUpdate(persistenceCtx, func(ctx context.Context, store Store) (bool, error) {
		return store.FinishJob(ctx, Lease{JobID: job.ID, StartedAt: startedAt}, JobOutcome{
			Status: JobFailed, Error: &message, DurationMS: durationMS, CompletedAt: finishedAt,
		})
	})
	if err != nil {
		return fmt.Errorf("metadata AI job failed with %q and the failure state could not be stored: %w", message, err)
	}
	if !finished {
		return fmt.Errorf("%w: %s", errMetadataAIJobLeaseUnavailable, job.ID)
	}

	job.Status = JobFailed
	job.Error = &message
	job.DurationMS = &durationMS
	job.CompletedAt = &finishedAt
	job.UpdatedAt = finishedAt

	return nil
}

func (m *MetadataJobManager) completeJob(
	ctx context.Context,
	job *Job,
	startedAt time.Time,
	suggestion map[string]string,
	responseText string,
) error {
	completedAt := time.Now()
	durationMS := time.Since(startedAt).Milliseconds()
	finished, err := m.runJobUpdate(ctx, func(ctx context.Context, store Store) (bool, error) {
		return store.FinishJob(ctx, Lease{JobID: job.ID, StartedAt: startedAt}, JobOutcome{
			Status: JobReady, Suggestion: suggestion, ResponseText: &responseText,
			DurationMS: durationMS, CompletedAt: completedAt,
		})
	})
	if err != nil {
		return fmt.Errorf("failed to finalize metadata AI job: %w", err)
	}
	if !finished {
		return fmt.Errorf("%w: %s", errMetadataAIJobLeaseUnavailable, job.ID)
	}

	job.Status = JobReady
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
