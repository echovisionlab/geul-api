package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/translation"
)

func (m *TranslationJobManager) applyAppliedTranslation(
	ctx context.Context,
	job *Job,
	candidate *translation.Candidate,
	now time.Time,
) error {
	handoffPrepared := false
	_, err := mutation.Run(ctx, m.mutations, mutation.StoreSpec[appliedTranslation, JobStore]{
		// The root lock stabilizes only the existence boundary.
		Lock: func(ctx context.Context, store JobStore) error {
			return store.Domains().LockRoot(ctx, job.EntityType, job.EntityID)
		},
		Access: runningJobAccess(job.ID),
		Change: func(ctx context.Context, store JobStore) (appliedTranslation, error) {
			return applyAppliedTranslationInStore(ctx, store, job, candidate, now)
		},
		// The owning domain appends its locale-content record when it applies
		// the candidate.
		Audit: func(appliedTranslation) mutation.Audit { return mutation.Unchanged() },
		Outbox: func(ctx context.Context, store JobStore, applied appliedTranslation) error {
			if err := publishAppliedTranslationContentUpdated(ctx, store, job, applied.target, now); err != nil {
				return err
			}
			handoffPrepared = applied.handoffPrepared
			return nil
		},
	})
	err = classifyTranslationOgHandoffTransactionError(err, handoffPrepared)
	if outcome, record := translationOgHandoffMetricOutcome(err, handoffPrepared); record {
		m.metrics.recordOgHandoff(ctx, job, outcome)
	}
	if err != nil {
		return err
	}
	job.Status = JobApplied
	job.CompletedAt = &now
	job.FailureReason = nil
	job.UpdatedAt = now
	return nil
}

func classifyTranslationOgHandoffTransactionError(err error, handoffPrepared bool) error {
	if err == nil || !handoffPrepared || errors.Is(err, errTranslationOgHandoffFailed) {
		return err
	}
	return fmt.Errorf("%w: transaction commit failed", errTranslationOgHandoffFailed)
}

func translationOgHandoffMetricOutcome(err error, handoffPrepared bool) (string, bool) {
	if err == nil {
		if handoffPrepared {
			return "committed", true
		}
		return "", false
	}
	if handoffPrepared || errors.Is(err, errTranslationOgHandoffFailed) {
		return "failed", true
	}
	return "", false
}

type appliedTranslation struct {
	target          AppliedTranslationTarget
	handoffPrepared bool
}

func applyAppliedTranslationInStore(
	ctx context.Context,
	store JobStore,
	job *Job,
	candidate *translation.Candidate,
	now time.Time,
) (appliedTranslation, error) {
	appliedTarget, err := persistAppliedTranslationCandidate(ctx, store, job, candidate, now)
	if err != nil {
		return appliedTranslation{}, markTranslationTargetApplyFailure(err)
	}
	prepared := false
	if appliedTarget.Changed {
		prepared, err = requestAppliedTranslationOg(ctx, store, job)
		if err != nil {
			return appliedTranslation{}, err
		}
	}
	if err := completeAppliedTranslationJob(ctx, store, job.ID); err != nil {
		return appliedTranslation{}, err
	}
	return appliedTranslation{target: appliedTarget, handoffPrepared: prepared}, nil
}

func lockRunningTranslationJob(ctx context.Context, store JobStore, jobID string) error {
	status, err := store.LockJobStatus(ctx, jobID)
	if errors.Is(err, ErrJobNotFound) {
		return errTranslationJobNoLongerCurrent
	}
	if err != nil {
		return err
	}
	if status != JobRunning {
		return errTranslationJobNoLongerCurrent
	}
	return nil
}

func persistAppliedTranslationCandidate(
	ctx context.Context,
	store JobStore,
	job *Job,
	candidate *translation.Candidate,
	now time.Time,
) (AppliedTranslationTarget, error) {
	entryInput := translation.EntryWrite{
		Title: candidate.Title, Summary: candidate.Summary,
		ContentJSON: candidate.ContentJSON, ContentHTML: candidate.ContentHTML, ContentText: candidate.ContentText,
		Now: now,
	}
	return store.Domains().ApplyCandidate(ctx, *job, candidate, entryInput)
}

func requestAppliedTranslationOg(ctx context.Context, store JobStore, job *Job) (bool, error) {
	reason := job.EntityType + "_machine_translation_applied"
	prepared, err := store.Domains().RequestLocaleOG(ctx, job.EntityType, job.EntityID, job.TargetLocale, reason)
	if err != nil {
		return false, fmt.Errorf("%w: %w", errTranslationOgHandoffFailed, err)
	}
	return prepared, nil
}

func completeAppliedTranslationJob(ctx context.Context, store JobStore, jobID string) error {
	deleted, err := store.DeleteJob(ctx, jobID, JobRunning)
	if err != nil {
		return err
	}
	if deleted == 0 {
		return errTranslationJobNoLongerCurrent
	}
	return nil
}

func (m *TranslationJobManager) applyAppliedTranslationResult(
	ctx context.Context,
	job *Job,
	candidate *translation.Candidate,
	now time.Time,
) (bool, error) {
	err := m.applyAppliedTranslation(ctx, job, candidate, now)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, errTranslationJobNoLongerCurrent) {
		return false, nil
	}
	return false, err
}

func (m *TranslationJobManager) publishLifecycle(
	ctx context.Context,
	job *Job,
	status LifecycleStatus,
	failureReason *string,
) {
	if job == nil {
		return
	}
	if _, ok := translation.DefinitionForKind(job.EntityType); !ok {
		slog.WarnContext(ctx, "Skipped lifecycle for unsupported translation entity", "job_id", job.ID, "entity_type", job.EntityType)
		return
	}
	event := LifecycleEvent{
		JobID:         job.ID,
		EntityType:    job.EntityType,
		EntityID:      job.EntityID,
		TargetLocale:  job.TargetLocale,
		Status:        status,
		FailureReason: failureReason,
		Timestamp:     m.now().UTC(),
	}
	if err := m.lifecycle.PublishTranslationLifecycle(ctx, event); err != nil {
		slog.ErrorContext(ctx, "Failed to publish translation lifecycle event", "domain", "translation", "event", "translation.lifecycle.publish_failed", "job_id", job.ID, "operation_id", job.OperationID, "entity_type", job.EntityType, "target_locale", job.TargetLocale, "status", string(status), "failure_cause", failurecause.Message(err))
	}
}
