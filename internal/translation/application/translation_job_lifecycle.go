package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/translation"
)

func (m *TranslationJobManager) loadTranslationJob(ctx context.Context, jobID string) (*Job, error) {
	var job Job
	var found bool
	err := m.units.Read(ctx, func(store JobStore) error {
		var err error
		job, found, err = store.Job(ctx, jobID)
		return err
	})
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	if err := ValidateJobRequester(&job); err != nil {
		return nil, err
	}
	return &job, nil
}

func (m *TranslationJobManager) claimTranslationJob(ctx context.Context, jobID string, generator translation.Generator) (bool, error) {
	now := m.now().UTC()
	return m.runJobUpdate(ctx, func(ctx context.Context, store JobStore) (bool, error) {
		return store.ClaimJob(ctx, jobID, generator.ProviderName(), generator.ModelName(), now)
	})
}

func (m *TranslationJobManager) resumeTranslationJob(
	ctx context.Context,
	jobID string,
	generator translation.Generator,
) (bool, error) {
	now := m.now().UTC()
	return m.runJobUpdate(ctx, func(ctx context.Context, store JobStore) (bool, error) {
		return store.ResumeJob(ctx, jobID, generator.ProviderName(), generator.ModelName(), now)
	})
}

func (m *TranslationJobManager) updateRunningJobProvider(
	ctx context.Context,
	jobID string,
	generator translation.Generator,
) error {
	if generator == nil {
		return errTranslationProviderUnavailable
	}
	return runRunningJobStep(ctx, m.mutations, jobID, func(ctx context.Context, store JobStore) error {
		job, err := lockTranslationProviderDocumentJob(ctx, store, jobID)
		if err != nil {
			return err
		}
		if job.Status != JobRunning {
			return errTranslationJobNoLongerCurrent
		}
		hasProviderDocumentState := job.ProviderDocumentID != nil || job.ProviderDocumentKey != nil ||
			job.ProviderDocumentSubmittedAt != nil
		if hasProviderDocumentState {
			if job.ProviderDocumentID == nil || job.ProviderDocumentKey == nil || job.ProviderDocumentSubmittedAt == nil ||
				job.ProviderDocumentSubmittedAt.IsZero() || job.Provider == nil || job.Model == nil ||
				*job.Provider != generator.ProviderName() ||
				*job.Model != generator.ModelName() {
				return errTranslationProviderDocumentHandleMismatch
			}
		}
		return store.SetJobProvider(ctx, jobID, generator.ProviderName(), generator.ModelName(), m.now().UTC())
	})
}

func (m *TranslationJobManager) finishJob(
	ctx context.Context,
	job *Job,
	status JobStatus,
	failureReason *string,
	now time.Time,
) error {
	deleted, err := m.runJobUpdate(ctx, func(ctx context.Context, store JobStore) (bool, error) {
		rows, err := store.DeleteJob(ctx, job.ID, JobRunning)
		return rows != 0, err
	})
	if err != nil {
		return err
	}
	if !deleted {
		return errTranslationJobNoLongerCurrent
	}
	job.Status = status
	job.CompletedAt = &now
	job.UpdatedAt = now
	if failureReason == nil {
		job.FailureReason = nil
	} else {
		persistedReason := *failureReason
		job.FailureReason = &persistedReason
	}
	return nil
}

func (m *TranslationJobManager) failJob(
	ctx context.Context,
	job *Job,
	startedAt time.Time,
	cause error,
) error {
	now := m.now().UTC()
	reason := classifyTranslationFailure(cause)
	if job != nil {
		if err := m.finishJob(ctx, job, JobFailed, &reason, now); err != nil {
			if errors.Is(err, errTranslationJobNoLongerCurrent) {
				return nil
			}
			return fmt.Errorf("translation job failed and terminal cleanup could not be completed: %w", err)
		}
		m.publishLifecycle(ctx, job, LifecycleFailed, &reason)
		m.metrics.recordJobStatus(ctx, job, JobFailed)
		m.metrics.recordJobDuration(ctx, job, JobFailed, startedAt, now)
		emitTranslationJobTerminal(ctx, job, translationJobTerminalOutcomeFailed, reason, startedAt, now)
	}
	return nil
}

func (m *TranslationJobManager) failQueuedJob(
	ctx context.Context,
	job *Job,
	cause error,
) error {
	if job == nil {
		return nil
	}
	now := m.now().UTC()
	reason := classifyTranslationFailure(cause)
	deleted, err := m.runJobUpdate(ctx, func(ctx context.Context, store JobStore) (bool, error) {
		rows, err := store.DeleteJob(ctx, job.ID, JobQueued)
		return rows != 0, err
	})
	if err != nil {
		return err
	}
	if !deleted {
		return nil
	}
	job.Status = JobFailed
	job.CompletedAt = &now
	job.UpdatedAt = now
	job.FailureReason = &reason
	m.publishLifecycle(ctx, job, LifecycleFailed, &reason)
	m.metrics.recordJobStatus(ctx, job, JobFailed)
	startedAt := translationJobStartedAt(job, now)
	m.metrics.recordJobDuration(ctx, job, JobFailed, startedAt, now)
	emitTranslationJobTerminal(ctx, job, translationJobTerminalOutcomeFailed, reason, startedAt, now)
	return nil
}

// runRunningJobStep runs one bookkeeping step of a running job in a
// mutation authorized by that job.
func runRunningJobStep(ctx context.Context, mutations *mutation.StoreRunner[JobStore], jobID string, step func(context.Context, JobStore) error) error {
	_, err := mutation.Run(ctx, mutations, mutation.StoreSpec[struct{}, JobStore]{
		Access: runningJobAccess(jobID),
		Change: func(ctx context.Context, store JobStore) (struct{}, error) {
			return struct{}{}, step(ctx, store)
		},
		// Translation jobs are operational queue rows, not audited content.
		Audit: func(struct{}) mutation.Audit { return mutation.Unchanged() },
	})
	return err
}
