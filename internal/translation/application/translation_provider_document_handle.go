package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/translation"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errTranslationProviderDocumentHandleMismatch = errors.New("translation provider document handle does not match the running job")

func (m *TranslationJobManager) persistTranslationProviderDocumentHandle(
	ctx context.Context,
	jobID string,
	provider string,
	modelName string,
	handle translation.ProviderDocumentHandle,
	submittedAt time.Time,
) error {
	if strings.TrimSpace(jobID) == "" || strings.TrimSpace(provider) == "" || strings.TrimSpace(modelName) == "" {
		return fmt.Errorf("translation provider document handle requires job, provider, and model identity")
	}
	if strings.TrimSpace(handle.DocumentID()) == "" || strings.TrimSpace(handle.DocumentKey()) == "" || submittedAt.IsZero() {
		return fmt.Errorf("translation provider document handle requires document identity and submission time")
	}

	return m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, err := lockRunningTranslationProviderDocumentJob(ctx, tx, jobID, provider, modelName)
		if err != nil {
			return err
		}
		if job.ProviderDocumentID != nil || job.ProviderDocumentKey != nil || job.ProviderDocumentSubmittedAt != nil {
			if job.ProviderDocumentID != nil && job.ProviderDocumentKey != nil && job.ProviderDocumentSubmittedAt != nil &&
				!job.ProviderDocumentSubmittedAt.IsZero() &&
				*job.ProviderDocumentID == handle.DocumentID() && *job.ProviderDocumentKey == handle.DocumentKey() {
				return nil
			}
			return errTranslationProviderDocumentHandleMismatch
		}

		now := m.now().UTC()
		result := tx.WithContext(ctx).
			Model(&model.TranslationJob{}).
			Where("id = ? AND status = ? AND provider = ? AND model = ?", jobID, translationJobStatusRunning, provider, modelName).
			Updates(structured.Fields{
				"provider_document_id":           handle.DocumentID(),
				"provider_document_key":          handle.DocumentKey(),
				"provider_document_submitted_at": submittedAt.UTC(),
				"updated_at":                     now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errTranslationJobNoLongerCurrent
		}
		return nil
	})
}

func (m *TranslationJobManager) clearExpiredTranslationProviderDocumentHandle(
	ctx context.Context,
	job *model.TranslationJob,
) error {
	if job == nil || job.Provider == nil || job.Model == nil ||
		job.ProviderDocumentID == nil || job.ProviderDocumentKey == nil ||
		job.ProviderDocumentSubmittedAt == nil {
		return errTranslationProviderDocumentHandleMismatch
	}
	result := m.db.WithContext(ctx).Model(&model.TranslationJob{}).
		Where(
			`id = ? AND status = ? AND provider = ? AND model = ?
				AND provider_document_id = ? AND provider_document_key = ?
				AND provider_document_submitted_at = ?`,
			job.ID,
			translationJobStatusRunning,
			*job.Provider,
			*job.Model,
			*job.ProviderDocumentID,
			*job.ProviderDocumentKey,
			job.ProviderDocumentSubmittedAt.UTC(),
		).
		Updates(structured.Fields{
			"provider_document_id":           nil,
			"provider_document_key":          nil,
			"provider_document_submitted_at": nil,
			"updated_at":                     m.now().UTC(),
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errTranslationJobNoLongerCurrent
	}
	job.ProviderDocumentID = nil
	job.ProviderDocumentKey = nil
	job.ProviderDocumentSubmittedAt = nil
	return nil
}

func lockRunningTranslationProviderDocumentJob(
	ctx context.Context,
	tx *gorm.DB,
	jobID string,
	provider string,
	modelName string,
) (*model.TranslationJob, error) {
	job, err := lockTranslationProviderDocumentJob(ctx, tx, jobID)
	if err != nil {
		return nil, err
	}
	if job.Status != translationJobStatusRunning || job.Provider == nil || job.Model == nil ||
		*job.Provider != provider || *job.Model != modelName {
		return nil, errTranslationJobNoLongerCurrent
	}
	return job, nil
}

func lockTranslationProviderDocumentJob(
	ctx context.Context,
	tx *gorm.DB,
	jobID string,
) (*model.TranslationJob, error) {
	var job model.TranslationJob
	err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "status", "provider", "model", "provider_document_id", "provider_document_key", "provider_document_submitted_at").
		First(&job, "id = ?", jobID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errTranslationJobNoLongerCurrent
		}
		return nil, err
	}
	return &job, nil
}
