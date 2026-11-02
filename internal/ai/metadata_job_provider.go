package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/llm"
)

func (m *MetadataJobManager) ProcessJob(ctx context.Context, jobID string) (retErr error) {
	var job Job
	defer func() {
		// The PGMQ consumer logs a returned failure, which can quote the job's
		// context and prompt.
		retErr = failurecause.WithPrivate(retErr, job.Context, job.Prompt)
	}()
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return fmt.Errorf("job ID is required")
	}

	if err := m.units.Read(ctx, func(store Store) error {
		var err error
		job, err = store.Job(ctx, jobID)
		return err
	}); err != nil {
		if errors.Is(err, ErrJobNotFound) {
			return nil
		}
		return fmt.Errorf("failed to load metadata AI job %s: %w", jobID, err)
	}
	if job.Status != JobQueued && job.Status != JobRunning {
		return nil
	}

	startedAt := dbtime.Now()
	staleBefore := startedAt.Add(-metadataAIJobLeaseDuration)
	if job.Status == JobRunning && job.StartedAt != nil && job.StartedAt.After(staleBefore) {
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

	responseSchema := buildMetadataResponseJSONSchema(ctx, userPrompt)
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
