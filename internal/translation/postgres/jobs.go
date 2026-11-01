package translationpostgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

var translationJobSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"requested_at":  "requested_at",
		"updated_at":    "updated_at",
		"target_locale": "target_locale",
		"status":        "status",
	},
	DefaultSort: "updated_at DESC, id ASC",
}

const (
	statusQueued  = string(application.JobQueued)
	statusRunning = string(application.JobRunning)
)

func (s *store) Job(ctx context.Context, jobID string) (application.Job, bool, error) {
	var job model.TranslationJob
	result := s.tx.WithContext(ctx).
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
		return application.Job{}, false, result.Error
	}
	if result.RowsAffected == 0 {
		return application.Job{}, false, nil
	}
	return jobOf(job), true, nil
}

func (s *store) JobForCommand(ctx context.Context, jobID string, locked bool) (application.Job, error) {
	query := s.tx.WithContext(ctx)
	if locked {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var job model.TranslationJob
	if err := query.First(&job, "id = ?", jobID).Error; err != nil {
		return application.Job{}, notFound(err, application.ErrJobNotFound)
	}
	return jobOf(job), nil
}

func (s *store) RequestArtifact(ctx context.Context, jobID string) (application.StoredRequestArtifact, error) {
	var row struct {
		XLIFF    []byte `gorm:"column:request_xliff"`
		Manifest []byte `gorm:"column:request_manifest"`
		Digest   string `gorm:"column:request_artifact_digest"`
	}
	err := s.tx.WithContext(ctx).Raw(
		`SELECT request_xliff, request_manifest, request_artifact_digest FROM translation_job WHERE id = ?`,
		jobID,
	).Take(&row).Error
	if err != nil {
		return application.StoredRequestArtifact{}, notFound(err, application.ErrJobNotFound)
	}
	return application.StoredRequestArtifact{XLIFF: row.XLIFF, Manifest: row.Manifest, Digest: row.Digest}, nil
}

func (s *store) ClaimJob(ctx context.Context, jobID, provider, modelName string, now time.Time) (bool, error) {
	result := s.tx.WithContext(ctx).Exec(
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
		statusRunning,
		provider,
		modelName,
		now,
		now,
		jobID,
		statusQueued,
		provider,
		modelName,
	)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

func (s *store) ResumeJob(ctx context.Context, jobID, provider, modelName string, now time.Time) (bool, error) {
	result := s.tx.WithContext(ctx).Exec(
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
		provider,
		modelName,
		now,
		jobID,
		statusRunning,
		provider,
		modelName,
	)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

func (s *store) LockJobStatus(ctx context.Context, jobID string) (application.JobStatus, error) {
	var current struct {
		Status string `gorm:"column:status"`
	}
	result := s.tx.WithContext(ctx).
		Model(&model.TranslationJob{}).
		Select("status").
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", jobID).
		Take(&current)
	if result.Error != nil {
		return "", notFound(result.Error, application.ErrJobNotFound)
	}
	return application.JobStatus(current.Status), nil
}

func (s *store) LockJobProviderDocument(ctx context.Context, jobID string) (application.Job, error) {
	var job model.TranslationJob
	err := s.tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id", "status", "provider", "model", "provider_document_id", "provider_document_key", "provider_document_submitted_at").
		First(&job, "id = ?", jobID).Error
	if err != nil {
		return application.Job{}, notFound(err, application.ErrJobNotFound)
	}
	return jobOf(job), nil
}

func (s *store) SetJobProvider(ctx context.Context, jobID, provider, modelName string, now time.Time) error {
	return s.tx.WithContext(ctx).
		Model(&model.TranslationJob{}).
		Where("id = ? AND status = ?", jobID, statusRunning).
		Updates(structured.Fields{
			"provider":   provider,
			"model":      modelName,
			"updated_at": now,
		}).Error
}

func (s *store) SetJobProviderDocument(ctx context.Context, jobID string, document application.ProviderDocument, now time.Time) (bool, error) {
	result := s.tx.WithContext(ctx).
		Model(&model.TranslationJob{}).
		Where("id = ? AND status = ? AND provider = ? AND model = ?", jobID, statusRunning, document.Provider, document.Model).
		Updates(structured.Fields{
			"provider_document_id":           document.DocumentID,
			"provider_document_key":          document.DocumentKey,
			"provider_document_submitted_at": document.SubmittedAt,
			"updated_at":                     now,
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected != 0, nil
}

func (s *store) ClearJobProviderDocument(ctx context.Context, jobID string, document application.ProviderDocument, now time.Time) (bool, error) {
	result := s.tx.WithContext(ctx).Model(&model.TranslationJob{}).
		Where(
			`id = ? AND status = ? AND provider = ? AND model = ?
				AND provider_document_id = ? AND provider_document_key = ?
				AND provider_document_submitted_at = ?`,
			jobID,
			statusRunning,
			document.Provider,
			document.Model,
			document.DocumentID,
			document.DocumentKey,
			document.SubmittedAt,
		).
		Updates(structured.Fields{
			"provider_document_id":           nil,
			"provider_document_key":          nil,
			"provider_document_submitted_at": nil,
			"updated_at":                     now,
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

func (s *store) DeleteJob(ctx context.Context, jobID string, statuses ...application.JobStatus) (int64, error) {
	if len(statuses) == 0 {
		return 0, errors.New("translation job delete requires a status")
	}
	query := s.tx.WithContext(ctx)
	if len(statuses) == 1 {
		query = query.Where("id = ? AND status = ?", jobID, string(statuses[0]))
	} else {
		values := make([]string, 0, len(statuses))
		for _, status := range statuses {
			values = append(values, string(status))
		}
		query = query.Where("id = ? AND status IN ?", jobID, values)
	}
	result := query.Delete(&model.TranslationJob{})
	return result.RowsAffected, result.Error
}

func (s *store) LockActiveJobs(ctx context.Context, key application.ActiveJobKey) ([]application.Job, error) {
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
		key.EntityType, key.EntityID, key.TargetLocale, key.RequestArtifactDigest,
		statusQueued, statusRunning,
	}
	if err := s.tx.WithContext(ctx).Raw(selectSQL, args...).Scan(&activeJobs).Error; err != nil {
		return nil, err
	}
	return jobsOf(activeJobs), nil
}

func (s *store) InsertJob(ctx context.Context, job application.Job) (bool, error) {
	row := jobRow(job)
	inserted := s.tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if inserted.Error != nil {
		return false, inserted.Error
	}
	return inserted.RowsAffected == 1, nil
}

func (s *store) EnqueueGenerate(ctx context.Context, jobID string) error {
	return s.ports.Queue.EnqueueTranslationGenerateWithDB(ctx, s.tx, &managev1.TranslationGenerateEvent{JobId: jobID})
}

func (s *store) ListJobs(ctx context.Context, query application.JobListQuery) ([]application.Job, int64, error) {
	db := s.tx.WithContext(ctx).Model(&model.TranslationJob{})
	db, err := applyTranslationJobFilters(db, query.Filters)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	db, err = applyTranslationJobSort(db, query.Sorts)
	if err != nil {
		return nil, 0, err
	}
	var jobs []model.TranslationJob
	page := queryutil.Pagination{Limit: query.Limit, Offset: query.Offset}
	if err := page.Apply(db).Find(&jobs).Error; err != nil {
		return nil, 0, err
	}
	return jobsOf(jobs), total, nil
}

func applyTranslationJobSort(query *gorm.DB, sorts []*commonv1.SortSpec) (*gorm.DB, error) {
	query, err := translationJobSortConfig.ApplySort(query, sorts)
	if err != nil {
		return nil, err
	}
	if len(sorts) != 0 {
		// Explicit sort fields can tie. Preserve a unique order across offset
		// pages just as the default sort does.
		query = query.Order("id ASC")
	}
	return query, nil
}

func applyTranslationJobFilters(query *gorm.DB, filters []*commonv1.FilterSpec) (*gorm.DB, error) {
	for _, filter := range filters {
		if filter == nil {
			continue
		}
		value := strings.TrimSpace(filter.GetValue())
		switch filter.GetField() {
		case "entity_type", "entity_id", "target_locale", "source_locale":
			if filter.GetOp() != commonv1.FilterOp_FILTER_OP_EQ {
				return nil, invalidFilterOp(filter.GetField(), filter.GetOp().String())
			}
			query = query.Where(fmt.Sprintf("%s = ?", filter.GetField()), value)
		case "status":
			switch filter.GetOp() {
			case commonv1.FilterOp_FILTER_OP_EQ:
				query = query.Where(fmt.Sprintf("%s = ?", filter.GetField()), value)
			case commonv1.FilterOp_FILTER_OP_IN:
				values := make([]string, 0, len(filter.GetValues()))
				for _, item := range filter.GetValues() {
					if normalized := strings.TrimSpace(item); normalized != "" {
						values = append(values, normalized)
					}
				}
				if len(values) == 0 {
					return nil, domainerr.InvalidField(filter.GetField(), "IN filter requires at least one value")
				}
				query = query.Where(fmt.Sprintf("%s IN ?", filter.GetField()), values)
			default:
				return nil, invalidFilterOp(filter.GetField(), filter.GetOp().String())
			}
		default:
			return nil, domainerr.New(domainerr.Invalid, filter.GetField(), "invalid filter field: "+filter.GetField())
		}
	}
	return query, nil
}

func invalidFilterOp(field, op string) error {
	return domainerr.New(domainerr.Invalid, field, fmt.Sprintf("invalid filter operation '%s' for field '%s'", op, field))
}

func jobsOf(rows []model.TranslationJob) []application.Job {
	jobs := make([]application.Job, 0, len(rows))
	for _, row := range rows {
		jobs = append(jobs, jobOf(row))
	}
	return jobs
}

func jobOf(row model.TranslationJob) application.Job {
	return application.Job{
		ID:                          row.ID,
		EntityType:                  row.EntityType,
		EntityID:                    row.EntityID,
		TargetLocale:                row.TargetLocale,
		SourceLocale:                row.SourceLocale,
		RequestArtifactDigest:       row.RequestArtifactDigest,
		OperationID:                 row.OperationID,
		Status:                      application.JobStatus(row.Status),
		RequestedByMemberID:         row.RequestedByMemberID,
		Provider:                    row.Provider,
		Model:                       row.Model,
		ProviderDocumentID:          row.ProviderDocumentID,
		ProviderDocumentKey:         row.ProviderDocumentKey,
		ProviderDocumentSubmittedAt: row.ProviderDocumentSubmittedAt,
		RequestXLIFF:                row.RequestXLIFF,
		RequestManifest:             row.RequestManifest,
		RequestedAt:                 row.RequestedAt,
		StartedAt:                   row.StartedAt,
		CreatedAt:                   row.CreatedAt,
		UpdatedAt:                   row.UpdatedAt,
	}
}

func jobRow(job application.Job) model.TranslationJob {
	return model.TranslationJob{
		ID:                          job.ID,
		EntityType:                  job.EntityType,
		EntityID:                    job.EntityID,
		TargetLocale:                job.TargetLocale,
		SourceLocale:                job.SourceLocale,
		RequestArtifactDigest:       job.RequestArtifactDigest,
		OperationID:                 job.OperationID,
		Status:                      string(job.Status),
		RequestedByMemberID:         job.RequestedByMemberID,
		Provider:                    job.Provider,
		Model:                       job.Model,
		ProviderDocumentID:          job.ProviderDocumentID,
		ProviderDocumentKey:         job.ProviderDocumentKey,
		ProviderDocumentSubmittedAt: job.ProviderDocumentSubmittedAt,
		RequestXLIFF:                job.RequestXLIFF,
		RequestManifest:             job.RequestManifest,
		RequestedAt:                 job.RequestedAt,
		StartedAt:                   job.StartedAt,
		CreatedAt:                   job.CreatedAt,
		UpdatedAt:                   job.UpdatedAt,
	}
}
