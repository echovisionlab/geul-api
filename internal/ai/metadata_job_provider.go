package ai

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/llm"
	"gorm.io/gorm"
)

func (m *MetadataJobManager) ProcessJob(ctx context.Context, jobID string) error {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return fmt.Errorf("job ID is required")
	}

	var job metadataJobRecord
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
