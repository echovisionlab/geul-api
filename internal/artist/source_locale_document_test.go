//go:build integration

package artist

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestArtistSourceLocaleMetadataUsesRootAuthorityIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	stack := testutil.SetupOryStack(t)
	store := testutil.NewEmailContentBlockStore(t, stack.SpiceDBClient)
	artistID := seedInternalArtist(t, db, store)
	now := time.Unix(1_700_000_000, 0).UTC()
	title := "Root authority"
	require.NoError(t, saveArtistSourceLocaleDocumentState(t.Context(), db, artistID, "en", translationLocaleDocumentSaveInput{
		SourceLocale: "en", Title: &title, Now: now,
	}))
	var persisted string
	require.NoError(t, db.Raw(
		`SELECT source_locale FROM artist WHERE id = ?::uuid`, artistID,
	).Scan(&persisted).Error)
	require.Equal(t, "en", persisted)
	var persistedTitle string
	require.NoError(t, db.Raw(
		`SELECT title FROM artist_translation WHERE entity_id = ?::uuid AND locale = 'en'`, artistID,
	).Scan(&persistedTitle).Error)
	require.Equal(t, title, persistedTitle)
}
