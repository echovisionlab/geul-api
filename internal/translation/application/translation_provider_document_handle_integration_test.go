//go:build integration

package application

import (
	"context"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/stretchr/testify/require"
)

func TestTranslationProviderDocumentHandleLifecycleIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	ctx := context.Background()
	now := time.Unix(1_700_001_000, 0).UTC()
	job, _ := seedRunningMenuTranslationJob(t, db, now)
	manager := &TranslationJobManager{
		db:  db,
		now: func() time.Time { return now.Add(time.Minute) },
	}
	generator := namedStubTranslationGenerator{providerName: "deepl", modelName: "document-v2.1"}
	require.NoError(t, manager.updateRunningJobProvider(ctx, job.ID, generator))

	submittedAt := now.Add(10 * time.Second)
	handle, err := translation.NewProviderDocumentHandle("document-1", "secret-document-key-1")
	require.NoError(t, err)
	require.NoError(t, manager.persistTranslationProviderDocumentHandle(
		ctx, job.ID, generator.ProviderName(), generator.ModelName(), handle, submittedAt,
	))
	require.ErrorIs(t, manager.persistTranslationProviderDocumentHandle(
		ctx, job.ID, generator.ProviderName(), "different-model", handle, submittedAt,
	), errTranslationJobNoLongerCurrent)
	require.ErrorIs(t, manager.updateRunningJobProvider(
		ctx,
		job.ID,
		namedStubTranslationGenerator{providerName: "another-provider", modelName: "another-model"},
	), errTranslationProviderDocumentHandleMismatch)
	resumed, err := manager.resumeTranslationJob(
		ctx,
		job.ID,
		namedStubTranslationGenerator{providerName: "another-provider", modelName: "another-model"},
	)
	require.NoError(t, err)
	require.False(t, resumed)

	loaded, err := manager.loadTranslationJob(ctx, job.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.Equal(t, handle.DocumentID(), requireStringValue(t, loaded.ProviderDocumentID))
	require.Equal(t, handle.DocumentKey(), requireStringValue(t, loaded.ProviderDocumentKey))
	require.WithinDuration(t, submittedAt, requireProviderDocumentSubmittedAt(t, loaded.ProviderDocumentSubmittedAt), 0)
	require.Error(t, db.Exec(
		"UPDATE translation_job SET provider_document_key = NULL WHERE id = ?",
		job.ID,
	).Error)

	// A retry of the same upload is idempotent and retains the authoritative
	// timestamp from the first successful submission.
	require.NoError(t, manager.persistTranslationProviderDocumentHandle(
		ctx, job.ID, generator.ProviderName(), generator.ModelName(), handle, submittedAt.Add(time.Minute),
	))
	loaded, err = manager.loadTranslationJob(ctx, job.ID)
	require.NoError(t, err)
	require.WithinDuration(t, submittedAt, requireProviderDocumentSubmittedAt(t, loaded.ProviderDocumentSubmittedAt), 0)

	differentHandle, err := translation.NewProviderDocumentHandle("document-1", "secret-document-key-2")
	require.NoError(t, err)
	require.ErrorIs(t, manager.persistTranslationProviderDocumentHandle(
		ctx, job.ID, generator.ProviderName(), generator.ModelName(), differentHandle, submittedAt,
	), errTranslationProviderDocumentHandleMismatch)
	// Provider recovery state exists only while the job is in flight.
	require.NoError(t, manager.persistTranslationProviderDocumentHandle(
		ctx, job.ID, generator.ProviderName(), generator.ModelName(), handle, submittedAt,
	))
	require.NoError(t, manager.finishJob(ctx, job, translationJobStatusApplied, nil, now.Add(2*time.Minute)))
	loaded, err = manager.loadTranslationJob(ctx, job.ID)
	require.NoError(t, err)
	require.Nil(t, loaded)

}

func requireProviderDocumentSubmittedAt(t *testing.T, value *time.Time) time.Time {
	t.Helper()
	require.NotNil(t, value)
	return *value
}
