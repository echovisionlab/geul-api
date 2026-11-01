package application

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
)

// CancelTranslationJobCommand cancels a queued or running translation job.
type CancelTranslationJobCommand struct {
	JobID string
}

// CancelTranslationJob cancels an in-flight job under the entity's
// regeneration authority.
func (s *Service) CancelTranslationJob(ctx context.Context, command CancelTranslationJobCommand) error {
	outcome := "failed"
	defer func() { s.metrics.recordAdminAction(ctx, "cancel", outcome) }()
	if _, err := requireAuthenticatedTranslationRequester(ctx); err != nil {
		return err
	}
	jobID := strings.TrimSpace(command.JobID)
	if jobID == "" {
		return domainerr.InvalidField("job_id", "job_id is required")
	}
	now := s.now().UTC()
	var candidate model.TranslationJob
	job, err := mutation.Run(ctx, s.mutations, mutation.Spec[model.TranslationJob]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			var err error
			if candidate, err = loadTranslationJobForCommand(ctx, tx, jobID, false); err != nil {
				return err
			}
			return s.domains.LockRoot(ctx, tx, candidate.EntityType, candidate.EntityID)
		},
		Access: mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
			return s.requireTranslationRegenerationAuthority(ctx, tx, candidate.EntityType, candidate.EntityID)
		}),
		Change: func(ctx context.Context, tx *gorm.DB) (model.TranslationJob, error) {
			job, err := loadTranslationJobForCommand(ctx, tx, jobID, true)
			if err != nil {
				return model.TranslationJob{}, err
			}
			result := tx.WithContext(ctx).
				Where("id = ? AND status IN ?", job.ID, []string{translationJobStatusQueued, translationJobStatusRunning}).
				Delete(&model.TranslationJob{})
			if result.Error != nil {
				return model.TranslationJob{}, result.Error
			}
			if result.RowsAffected != 1 {
				return model.TranslationJob{}, domainerr.Precondition("translation job is already terminal")
			}
			job.Status = translationJobStatusCancelled
			job.CompletedAt = &now
			job.UpdatedAt = now
			return job, nil
		},
		// Translation jobs are operational queue rows, not audited content.
		Audit: func(model.TranslationJob) mutation.Audit { return mutation.Unchanged() },
	})
	if err != nil {
		return err
	}
	emitTranslationJobTerminal(
		ctx, &job, translationJobTerminalOutcomeCancelled, "", translationJobStartedAt(&job, now), now,
	)
	outcome = "succeeded"
	return nil
}

func requireAuthenticatedTranslationRequester(ctx context.Context) (string, error) {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || strings.TrimSpace(user.MemberID.String()) == "" {
		return "", domainerr.AuthenticationRequired()
	}
	if user.Banned {
		return "", domainerr.AccountBanned()
	}
	if !user.Onboarded {
		return "", domainerr.NoPermission("edit", "translation")
	}
	return user.MemberID.String(), nil
}

func loadTranslationJobForCommand(
	ctx context.Context,
	db *gorm.DB,
	jobID string,
	locked bool,
) (model.TranslationJob, error) {
	if jobID == "" {
		return model.TranslationJob{}, domainerr.InvalidField("job_id", "job_id is required")
	}
	query := db.WithContext(ctx)
	if locked {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var job model.TranslationJob
	if err := query.First(&job, "id = ?", jobID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.TranslationJob{}, domainerr.ResourceNotFound("translation_job", jobID)
		}
		return model.TranslationJob{}, err
	}
	if err := validateTranslationJobRequester(&job); err != nil {
		return model.TranslationJob{}, err
	}
	return job, nil
}

func (s *Service) requireAvailableTranslationProvider(ctx context.Context) error {
	available, err := hasAvailableTranslationProvider(ctx, s.db)
	if err != nil {
		return err
	}
	if !available {
		return domainerr.Precondition(translationProviderUnavailableMessage)
	}
	return nil
}

func lockActiveTranslationRetryJobs(tx *gorm.DB, job model.TranslationJob) ([]model.TranslationJob, error) {
	if err := validateTranslationJobRequestArtifact(&job); err != nil {
		return nil, err
	}
	var activeJobs []model.TranslationJob
	selectSQL := `SELECT id, entity_type, entity_id, target_locale, source_locale,
		       request_artifact_digest,
		       operation_id, status, requested_by_member_id, provider, model,
		       provider_document_id, provider_document_key, provider_document_submitted_at,
		       requested_at, started_at, created_at, updated_at
		FROM translation_job
		WHERE entity_type = ? AND entity_id = ? AND target_locale = ?
		  AND request_artifact_digest = ? AND status IN (?, ?)
		ORDER BY requested_at ASC, id ASC
		FOR UPDATE`
	args := []any{
		job.EntityType, job.EntityID, job.TargetLocale, job.RequestArtifactDigest,
		translationJobStatusQueued, translationJobStatusRunning,
	}
	result := tx.Raw(selectSQL, args...).Scan(&activeJobs)
	return activeJobs, result.Error
}
