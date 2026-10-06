//go:build integration

package sitemapadapter

import (
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSitemapPageAudienceExcludesRestrictedPagesAndHomepageIntegration(t *testing.T) {
	db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{}).DB
	publicID, authenticatedID, conditionsID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, fixture := range []struct{ id, policy string }{{publicID, "{}"}, {authenticatedID, `{"mode":"PAGE_ACCESS_MODE_AUTHENTICATED"}`}, {conditionsID, `{"mode":"PAGE_ACCESS_MODE_CONDITIONS","allowedRoles":["AUTHOR"]}`}} {
		documentID := uuid.NewString()
		require.NoError(t, db.Exec(`INSERT INTO content_document(id,profile) VALUES(?::uuid,'page')`, documentID).Error)
		require.NoError(t, db.Exec(`INSERT INTO page(id,slug,status,content_document_id,access_policy,published_at) VALUES(?::uuid,?,'PAGE_STATUS_PUBLISHED',?::uuid,?::jsonb,now())`, fixture.id, fixture.id, documentID, fixture.policy).Error)
	}
	store := NewPostgresStore(db)
	entries, err := store.ListPages(t.Context())
	require.NoError(t, err)
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ID)
	}
	require.Contains(t, ids, publicID)
	require.NotContains(t, ids, authenticatedID)
	require.NotContains(t, ids, conditionsID)
	for _, id := range []string{authenticatedID, conditionsID} {
		entry, loadErr := store.LoadHomepage(t.Context(), id)
		require.NoError(t, loadErr)
		require.Nil(t, entry)
	}
	require.NoError(t, db.Exec(`UPDATE page SET access_policy='{}'::jsonb WHERE id=?::uuid`, authenticatedID).Error)
	entry, err := store.LoadHomepage(t.Context(), authenticatedID)
	require.NoError(t, err)
	require.NotNil(t, entry)
	entries, err = store.ListPages(t.Context())
	require.NoError(t, err)
	ids = nil
	for _, entry := range entries {
		ids = append(ids, entry.ID)
	}
	require.Contains(t, ids, authenticatedID)
}
