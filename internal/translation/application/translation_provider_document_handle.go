package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/translation"
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

	return runRunningJobStep(ctx, m.mutations, jobID, func(ctx context.Context, store JobStore) error {
		job, err := lockRunningTranslationProviderDocumentJob(ctx, store, jobID, provider, modelName)
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

		updated, err := store.SetJobProviderDocument(ctx, jobID, ProviderDocument{
			Provider: provider, Model: modelName,
			DocumentID: handle.DocumentID(), DocumentKey: handle.DocumentKey(), SubmittedAt: submittedAt.UTC(),
		}, m.now().UTC())
		if err != nil {
			return err
		}
		if !updated {
			return errTranslationJobNoLongerCurrent
		}
		return nil
	})
}

func (m *TranslationJobManager) clearExpiredTranslationProviderDocumentHandle(
	ctx context.Context,
	job *Job,
) error {
	if job == nil || job.Provider == nil || job.Model == nil ||
		job.ProviderDocumentID == nil || job.ProviderDocumentKey == nil ||
		job.ProviderDocumentSubmittedAt == nil {
		return errTranslationProviderDocumentHandleMismatch
	}
	document := ProviderDocument{
		Provider: *job.Provider, Model: *job.Model,
		DocumentID: *job.ProviderDocumentID, DocumentKey: *job.ProviderDocumentKey,
		SubmittedAt: job.ProviderDocumentSubmittedAt.UTC(),
	}
	now := m.now().UTC()
	cleared, err := m.runJobUpdate(ctx, func(ctx context.Context, store JobStore) (bool, error) {
		return store.ClearJobProviderDocument(ctx, job.ID, document, now)
	})
	if err != nil {
		return err
	}
	if !cleared {
		return errTranslationJobNoLongerCurrent
	}
	job.ProviderDocumentID = nil
	job.ProviderDocumentKey = nil
	job.ProviderDocumentSubmittedAt = nil
	return nil
}

func lockRunningTranslationProviderDocumentJob(
	ctx context.Context,
	store JobStore,
	jobID string,
	provider string,
	modelName string,
) (*Job, error) {
	job, err := lockTranslationProviderDocumentJob(ctx, store, jobID)
	if err != nil {
		return nil, err
	}
	if job.Status != JobRunning || job.Provider == nil || job.Model == nil ||
		*job.Provider != provider || *job.Model != modelName {
		return nil, errTranslationJobNoLongerCurrent
	}
	return job, nil
}

func lockTranslationProviderDocumentJob(
	ctx context.Context,
	store JobStore,
	jobID string,
) (*Job, error) {
	job, err := store.LockJobProviderDocument(ctx, jobID)
	if errors.Is(err, ErrJobNotFound) {
		return nil, errTranslationJobNoLongerCurrent
	}
	if err != nil {
		return nil, err
	}
	return &job, nil
}
