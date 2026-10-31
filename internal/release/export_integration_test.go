//go:build integration

package release

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
)

// SeedReleaseTargetLocaleForTest explicitly creates a Release target locale the
// way translation delivery does, so external tests can exercise the edit paths
// that refuse to create one. It returns the new target revision.
func SeedReleaseTargetLocaleForTest(
	t *testing.T,
	db *gorm.DB,
	store *contentblock.Store,
	releaseID, locale, title string,
	notes map[string]string,
) string {
	t.Helper()
	ctx := t.Context()
	documentID, err := loadReleaseContentDocumentID(ctx, db, releaseID)
	require.NoError(t, err)
	var revisionText string
	require.NoError(t, db.Raw(`SELECT revision::text FROM content_document WHERE id = ?`, documentID).Scan(&revisionText).Error)
	revision, err := uuid.Parse(revisionText)
	require.NoError(t, err)
	var result releaseTargetLocaleMutationResult
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var applyErr error
		result, applyErr = applyReleaseTargetLocaleMutation(ctx, tx, store, releaseTargetLocaleMutationInput{
			ReleaseID: releaseID, DocumentID: documentID, Locale: locale,
			ExpectedDocumentRevision: revision, Policy: targetlocale.Policy{AllowCreate: true, SeedSource: true},
			Batch:    contentblock.Batch{DocumentID: documentID, ExpectedRevision: revision},
			SetTitle: true, Title: &title, CreditNotePatch: notes,
			Now: time.Now().UTC(), Fence: internalReleaseContentFence(releaseID),
		})
		return applyErr
	}))
	require.True(t, result.LocaleCreated)
	return result.TargetRevision
}
