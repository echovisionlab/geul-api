package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (m *TranslationJobManager) applyAppliedTranslation(
	ctx context.Context,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	now time.Time,
) error {
	handoffPrepared := false
	err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return m.applyAppliedTranslationWithDB(ctx, tx, job, candidate, now, &handoffPrepared)
	})
	err = classifyTranslationOgHandoffTransactionError(err, handoffPrepared)
	if outcome, record := translationOgHandoffMetricOutcome(err, handoffPrepared); record {
		m.metrics.recordOgHandoff(ctx, job, outcome)
	}
	if err != nil {
		return err
	}
	job.Status = translationJobStatusApplied
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

func (m *TranslationJobManager) applyAppliedTranslationWithDB(
	ctx context.Context,
	tx *gorm.DB,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	now time.Time,
	handoffPrepared *bool,
) error {
	if err := m.lockTranslationJobApplyRoot(ctx, tx, job); err != nil {
		return err
	}
	if err := lockRunningTranslationJob(ctx, tx, job.ID); err != nil {
		return err
	}
	appliedTarget, err := m.persistAppliedTranslationCandidate(ctx, tx, job, candidate, now)
	if err != nil {
		return markTranslationTargetApplyFailure(err)
	}
	prepared := false
	if appliedTarget.Changed {
		prepared, err = m.requestAppliedTranslationOg(ctx, tx, job)
		if err != nil {
			return err
		}
	}
	if err := completeAppliedTranslationJob(ctx, tx, job.ID); err != nil {
		return err
	}
	if err := m.publishAppliedTranslationContentUpdated(
		ctx,
		tx,
		job,
		appliedTarget,
		now,
	); err != nil {
		return err
	}
	*handoffPrepared = prepared
	return nil
}

// lockTranslationJobApplyRoot stabilizes only the root existence boundary.
// Requester identity, current relationships, and lifecycle are intentionally
// not re-authorized after the request transaction accepted the job.
func (m *TranslationJobManager) lockTranslationJobApplyRoot(ctx context.Context, tx *gorm.DB, job *model.TranslationJob) error {
	if m.domains == nil {
		return fmt.Errorf("translation domain registry is required")
	}
	return m.domains.LockRoot(ctx, tx, job.EntityType, job.EntityID)
}

func lockRunningTranslationJob(ctx context.Context, tx *gorm.DB, jobID string) error {
	var current struct {
		Status string `gorm:"column:status"`
	}
	result := tx.WithContext(ctx).
		Model(&model.TranslationJob{}).
		Select("status").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", jobID).
		Take(&current)
	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return errTranslationJobNoLongerCurrent
		}
		return result.Error
	}
	if result.RowsAffected == 0 || current.Status != translationJobStatusRunning {
		return errTranslationJobNoLongerCurrent
	}
	return nil
}

func (m *TranslationJobManager) persistAppliedTranslationCandidate(
	ctx context.Context,
	tx *gorm.DB,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	now time.Time,
) (AppliedTranslationTarget, error) {
	entryInput := translation.EntryWrite{
		Title: candidate.Title, Summary: candidate.Summary,
		ContentJSON: candidate.ContentJSON, ContentHTML: candidate.ContentHTML, ContentText: candidate.ContentText,
		Now: now,
	}
	if m.domains == nil {
		return AppliedTranslationTarget{}, fmt.Errorf("translation domain registry is required")
	}
	return m.domains.ApplyCandidate(
		ctx, tx, m.contentBlocks, job, candidate, entryInput,
	)
}

func (m *TranslationJobManager) requestAppliedTranslationOg(ctx context.Context, tx *gorm.DB, job *model.TranslationJob) (bool, error) {
	reason := job.EntityType + "_machine_translation_applied"
	if m.domains == nil {
		return false, fmt.Errorf("translation domain registry is required")
	}
	prepared, err := m.domains.RequestLocaleOG(
		ctx, tx, m.ogPlanner, m.ogRefresher,
		job.EntityType, job.EntityID, job.TargetLocale, reason,
	)
	if err != nil {
		return false, fmt.Errorf("%w: %w", errTranslationOgHandoffFailed, err)
	}
	return prepared, nil
}

func completeAppliedTranslationJob(ctx context.Context, tx *gorm.DB, jobID string) error {
	result := tx.WithContext(ctx).
		Where("id = ? AND status = ?", jobID, translationJobStatusRunning).
		Delete(&model.TranslationJob{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errTranslationJobNoLongerCurrent
	}
	return nil
}

func (m *TranslationJobManager) applyAppliedTranslationResult(
	ctx context.Context,
	job *model.TranslationJob,
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
	job *model.TranslationJob,
	status managev1.TranslationLifecycleStatus,
	failureReason *string,
) {
	if job == nil {
		return
	}
	definition, ok := translation.DefinitionForKind(job.EntityType)
	if !ok {
		slog.Warn("Skipped lifecycle for unsupported translation entity", "job_id", job.ID, "entity_type", job.EntityType)
		return
	}
	event := &managev1.TranslationLifecycleEvent{
		JobId:        job.ID,
		EntityType:   definition.Proto,
		EntityId:     job.EntityID,
		TargetLocale: job.TargetLocale,
		Status:       status,
		TimestampMs:  m.now().UTC().UnixMilli(),
	}
	event.FailureReason = toProtoTranslationFailureReason(failureReason)
	if err := m.publisher.PublishTranslationLifecycle(ctx, event); err != nil {
		slog.Warn("Failed to publish translation lifecycle event", "job_id", job.ID, "status", status.String(), "error", err)
	}
}
