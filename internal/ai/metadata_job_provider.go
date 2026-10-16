package ai

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/llm"
	"gorm.io/gorm"
)

func (m *MetadataJobManager) ProcessJob(ctx context.Context, jobID string) (retErr error) {
	var job metadataJobRecord
	defer func() {
		if retErr == nil {
			return
		}
		level := slog.LevelError
		if errors.Is(retErr, context.Canceled) || errors.Is(retErr, errMetadataAIJobLeaseUnavailable) {
			level = slog.LevelWarn
		}
		slog.Log(ctx, level, "Metadata AI job processing failed", "domain", "ai", "event", "ai.metadata.delivery_failed", "job_id", jobID, "target_type", job.TargetType, "target_id", job.TargetID, failurecause.Attr(retErr, job.Context, job.Prompt))
	}()
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return fmt.Errorf("job ID is required")
	}

	if err := m.db.WithContext(ctx).First(&job, "id = ?", jobID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil
		}
		return fmt.Errorf("failed to load metadata AI job %s: %w", jobID, err)
	}
	if job.Status != metadataAIJobStatusQueued && job.Status != metadataAIJobStatusRunning {
		return nil
	}

	startedAt := time.Now().UTC().Truncate(time.Microsecond)
	staleBefore := startedAt.Add(-metadataAIJobLeaseDuration)
	if job.Status == metadataAIJobStatusRunning && job.StartedAt != nil && job.StartedAt.After(staleBefore) {
		return fmt.Errorf("%w: %s", errMetadataAIJobLeaseUnavailable, job.ID)
	}
	claimed, err := m.claimJob(ctx, &job, startedAt)
	if err != nil {
		return err
	}
	if !claimed {
		return fmt.Errorf("%w: %s", errMetadataAIJobLeaseUnavailable, job.ID)
	}

	userPrompt := metadataAIUserPrompt(job.Context, job.Prompt)

	responseSchema := buildMetadataResponseJSONSchema(userPrompt)
	responseText, err := m.provider.GenerateText(ctx, llm.GenerationRequest{
		RequestID:          job.ID,
		Action:             "metadata-json",
		SystemPrompt:       metadataAISystemPrompt,
		UserPrompt:         userPrompt,
		ResponseJSONSchema: responseSchema,
		Timeout:            metadataAIProviderTimeout,
		Observer:           metadataAIProviderObserver{},
	})
	if err != nil {
		return m.failJob(ctx, &job, startedAt, time.Since(startedAt), err)
	}
	if err := ctx.Err(); err != nil {
		return m.failJob(ctx, &job, startedAt, time.Since(startedAt), err)
	}

	suggestion, err := parseMetadataSuggestionPayload(responseText, job.RequestedKeys)
	if err != nil {
		return m.failJob(ctx, &job, startedAt, time.Since(startedAt), err)
	}
	if err := ctx.Err(); err != nil {
		return m.failJob(ctx, &job, startedAt, time.Since(startedAt), err)
	}

	return m.completeJob(ctx, &job, startedAt, suggestion, responseText)
}
