package application

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (s *TranslationService) CancelTranslationJob(
	ctx context.Context,
	req *connect.Request[managev1.CancelTranslationJobRequest],
) (*connect.Response[managev1.CancelTranslationJobResponse], error) {
	outcome := "failed"
	defer func() { s.metrics.recordAdminAction(ctx, "cancel", outcome) }()
	if _, err := requireAuthenticatedTranslationRequester(ctx); err != nil {
		return nil, err
	}
	jobID := strings.TrimSpace(req.Msg.GetJobId())
	if jobID == "" {
		return nil, errs.InvalidArgument("job_id", "job_id is required")
	}
	var job model.TranslationJob
	changed := false
	now := s.now().UTC()
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		candidate, err := loadTranslationJobForCommand(ctx, tx, jobID, false)
		if err != nil {
			return err
		}
		if err := s.validateTranslationRegenerationWithDB(
			ctx, tx, candidate.EntityType, candidate.EntityID,
		); err != nil {
			return err
		}
		job, err = loadTranslationJobForCommand(ctx, tx, jobID, true)
		if err != nil {
			return err
		}
		result := tx.WithContext(ctx).
			Where("id = ? AND status IN ?", job.ID, []string{translationJobStatusQueued, translationJobStatusRunning}).
			Delete(&model.TranslationJob{})
		if result.Error != nil {
			return errs.Internal(result.Error)
		}
		if result.RowsAffected != 1 {
			return errs.FailedPrecondition("translation job is already terminal")
		}
		job.Status = translationJobStatusCancelled
		job.CompletedAt = &now
		job.UpdatedAt = now
		changed = true
		return nil
	}); err != nil {
		return nil, err
	}
	if changed {
		emitTranslationJobTerminal(
			ctx,
			&job,
			translationJobTerminalOutcomeCancelled,
			"",
			translationJobStartedAt(&job, now),
			now,
		)
		outcome = "succeeded"
	} else {
		outcome = "unchanged"
	}
	return connect.NewResponse(&managev1.CancelTranslationJobResponse{}), nil
}

func requireAuthenticatedTranslationRequester(ctx context.Context) (string, error) {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || strings.TrimSpace(user.MemberID.String()) == "" {
		return "", errs.AuthenticationRequired()
	}
	if user.Banned {
		return "", errs.AccountBanned()
	}
	if !user.Onboarded {
		return "", errs.NoPermission("edit", "translation")
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
		return model.TranslationJob{}, errs.InvalidArgument("job_id", "job_id is required")
	}
	query := db.WithContext(ctx)
	if locked {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var job model.TranslationJob
	if err := query.First(&job, "id = ?", jobID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return model.TranslationJob{}, errs.NotFound("translation_job", jobID)
		}
		return model.TranslationJob{}, errs.Internal(err)
	}
	if err := validateTranslationJobRequester(&job); err != nil {
		return model.TranslationJob{}, errs.Internal(err)
	}
	return job, nil
}

func (s *TranslationService) requireAvailableTranslationProvider(ctx context.Context) error {
	available, err := hasAvailableTranslationProvider(ctx, s.db)
	if err != nil {
		return errs.Internal(err)
	}
	if !available {
		return errs.FailedPrecondition(translationProviderUnavailableMessage)
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
