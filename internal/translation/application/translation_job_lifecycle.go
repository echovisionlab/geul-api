package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

func (m *TranslationJobManager) loadTranslationJob(ctx context.Context, jobID string) (*model.TranslationJob, error) {
	var job model.TranslationJob
	result := m.db.WithContext(ctx).
		Raw(`SELECT id, entity_type, entity_id, target_locale, source_locale,
		             request_artifact_digest,
		             operation_id, status, requested_by_member_id, provider, model,
		             provider_document_id, provider_document_key, provider_document_submitted_at,
		             requested_at, started_at, created_at, updated_at
			FROM translation_job
			WHERE id = ?
			LIMIT 1`, jobID).
		Scan(&job)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	if err := validateTranslationJobRequester(&job); err != nil {
		return nil, err
	}
	return &job, nil
}

func (m *TranslationJobManager) claimTranslationJob(ctx context.Context, jobID string, generator translation.Generator) (bool, error) {
	now := m.now().UTC()
	result := m.db.WithContext(ctx).Exec(
		`UPDATE translation_job
		SET status = ?,
			provider = ?,
			model = ?,
			started_at = COALESCE(started_at, ?),
			updated_at = ?
		WHERE id = ? AND status = ?
			AND (
				(provider_document_id IS NULL AND provider_document_key IS NULL AND provider_document_submitted_at IS NULL)
				OR (
					provider_document_id IS NOT NULL
					AND provider_document_key IS NOT NULL
					AND provider_document_submitted_at IS NOT NULL
					AND provider = ?
					AND model = ?
				)
			)`,
		translationJobStatusRunning,
		generator.ProviderName(),
		generator.ModelName(),
		now,
		now,
		jobID,
		translationJobStatusQueued,
		generator.ProviderName(),
		generator.ModelName(),
	)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

func (m *TranslationJobManager) resumeTranslationJob(
	ctx context.Context,
	jobID string,
	generator translation.Generator,
) (bool, error) {
	now := m.now().UTC()
	result := m.db.WithContext(ctx).Exec(
		`UPDATE translation_job
		SET provider = ?,
			model = ?,
			updated_at = ?
		WHERE id = ? AND status = ?
			AND (
				(provider_document_id IS NULL AND provider_document_key IS NULL AND provider_document_submitted_at IS NULL)
				OR (
					provider_document_id IS NOT NULL
					AND provider_document_key IS NOT NULL
					AND provider_document_submitted_at IS NOT NULL
					AND provider = ?
					AND model = ?
				)
			)`,
		generator.ProviderName(),
		generator.ModelName(),
		now,
		jobID,
		translationJobStatusRunning,
		generator.ProviderName(),
		generator.ModelName(),
	)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

func (m *TranslationJobManager) updateRunningJobProvider(
	ctx context.Context,
	jobID string,
	generator translation.Generator,
) error {
	if generator == nil {
		return errTranslationProviderUnavailable
	}
	return m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, err := lockTranslationProviderDocumentJob(ctx, tx, jobID)
		if err != nil {
			return err
		}
		if job.Status != translationJobStatusRunning {
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
		return tx.WithContext(ctx).
			Model(&model.TranslationJob{}).
			Where("id = ? AND status = ?", jobID, translationJobStatusRunning).
			Updates(structured.Fields{
				"provider":   generator.ProviderName(),
				"model":      generator.ModelName(),
				"updated_at": m.now().UTC(),
			}).Error
	})
}

func (m *TranslationJobManager) finishJob(
	ctx context.Context,
	job *model.TranslationJob,
	status string,
	failureReason *string,
	now time.Time,
) error {
	result := m.db.WithContext(ctx).
		Where("id = ? AND status = ?", job.ID, translationJobStatusRunning).
		Delete(&model.TranslationJob{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
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
	job *model.TranslationJob,
	startedAt time.Time,
	cause error,
) error {
	now := m.now().UTC()
	reason := classifyTranslationFailure(cause)
	if job != nil {
		if err := m.finishJob(ctx, job, translationJobStatusFailed, &reason, now); err != nil {
			if errors.Is(err, errTranslationJobNoLongerCurrent) {
				return nil
			}
			return fmt.Errorf("translation job failed and terminal cleanup could not be completed: %w", err)
		}
		m.publishLifecycle(ctx, job, managev1.TranslationLifecycleStatus_TRANSLATION_LIFECYCLE_STATUS_FAILED, &reason)
		m.metrics.recordJobStatus(ctx, job, translationJobStatusFailed)
		m.metrics.recordJobDuration(ctx, job, translationJobStatusFailed, startedAt, now)
		emitTranslationJobTerminal(ctx, job, translationJobTerminalOutcomeFailed, reason, startedAt, now)
	}
	return nil
}

func (m *TranslationJobManager) failQueuedJob(
	ctx context.Context,
	job *model.TranslationJob,
	cause error,
) error {
	if job == nil {
		return nil
	}
	now := m.now().UTC()
	reason := classifyTranslationFailure(cause)
	result := m.db.WithContext(ctx).
		Where("id = ? AND status = ?", job.ID, translationJobStatusQueued).
		Delete(&model.TranslationJob{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return nil
	}
	job.Status = translationJobStatusFailed
	job.CompletedAt = &now
	job.UpdatedAt = now
	job.FailureReason = &reason
	m.publishLifecycle(ctx, job, managev1.TranslationLifecycleStatus_TRANSLATION_LIFECYCLE_STATUS_FAILED, &reason)
	m.metrics.recordJobStatus(ctx, job, translationJobStatusFailed)
	startedAt := translationJobStartedAt(job, now)
	m.metrics.recordJobDuration(ctx, job, translationJobStatusFailed, startedAt, now)
	emitTranslationJobTerminal(ctx, job, translationJobTerminalOutcomeFailed, reason, startedAt, now)
	return nil
}
