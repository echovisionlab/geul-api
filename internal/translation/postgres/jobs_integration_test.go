//go:build integration

package translationpostgres

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	"github.com/echovisionlab/geul-api/internal/translation/translationtest"
)

// The schema, not the Store, refuses half a provider document handle, so a
// recovery never resumes a document it cannot address.
func TestSchemaRefusesHalfAProviderDocumentHandleIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{BootstrapKratosStub: true, ApplyAppSchemaSQL: true}).DB
	now := time.Now().UTC().Truncate(time.Microsecond)
	job := translationtest.NewJob(testutil.SeedPostIntegrationIdentity(t, db, uuid.NewString(), "Translation requester"), now)
	store := &store{tx: db}
	inserted, err := store.InsertJob(t.Context(), job)
	require.NoError(t, err)
	require.True(t, inserted)
	claimed, err := store.ClaimJob(t.Context(), job.ID, "deepl", "document-v2.1", now)
	require.NoError(t, err)
	require.True(t, claimed)
	stored, err := store.SetJobProviderDocument(t.Context(), job.ID, application.ProviderDocument{
		Provider: "deepl", Model: "document-v2.1", DocumentID: "document-1", DocumentKey: "secret-key", SubmittedAt: now,
	}, now)
	require.NoError(t, err)
	require.True(t, stored)

	require.Error(t, db.Exec("UPDATE translation_job SET provider_document_key = NULL WHERE id = ?", job.ID).Error)
}
