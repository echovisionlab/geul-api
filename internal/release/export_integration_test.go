//go:build integration

package release

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
)

// SeedReleaseTargetLocaleForTest explicitly creates a Release target locale the
// way translation delivery does, so external tests can exercise the edit paths
// that refuse to create one. It returns the new target revision.
func SeedReleaseTargetLocaleForTest(
	t *testing.T,
	units mutation.UnitOfWork[Store],
	releaseID, locale, title string,
	notes map[string]string,
) string {
	t.Helper()
	ctx := t.Context()
	var result releaseTargetLocaleMutationResult
	require.NoError(t, units.Transaction(ctx, false, func(tx mutation.Tx[Store]) error {
		store := tx.Store()
		documentID, err := loadReleaseContentDocumentID(ctx, store, releaseID, Unlocked)
		if err != nil {
			return err
		}
		release, err := store.FindRelease(ctx, releaseID)
		if err != nil {
			return err
		}
		snapshot, err := store.ContentSnapshot(ctx, documentID, release.SourceLocale)
		if err != nil {
			return err
		}
		revision := snapshot.Document.Revision
		result, err = applyReleaseTargetLocaleMutation(ctx, store, releaseTargetLocaleMutationInput{
			ReleaseID: releaseID, DocumentID: documentID, Locale: locale,
			ExpectedDocumentRevision: revision, Policy: targetlocale.Policy{AllowCreate: true, SeedSource: true},
			Batch:    contentblock.Batch{DocumentID: documentID, ExpectedRevision: revision},
			SetTitle: true, Title: &title, CreditNotePatch: notes,
			Now: time.Now().UTC(), Fence: internalReleaseContentFence(releaseID),
		})
		return err
	}))
	require.True(t, result.LocaleCreated)
	return result.TargetRevision
}
