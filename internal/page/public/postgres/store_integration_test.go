//go:build integration

package pagepublicpostgres_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/page"
	pagepublic "github.com/echovisionlab/geul-api/internal/page/public"
	pagepublicpostgres "github.com/echovisionlab/geul-api/internal/page/public/postgres"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// The ports this test does not reach panic if called.
type unusedShareLinks struct{ pagepublicpostgres.ShareLinkAccessChecker }
type unusedFileReuse struct{ contentblock.FileReuseAuthorizer }

// The public read's own SQL: the homepage setting, the share-locked Page
// by ID and slug, its source metadata, and the locale selection over the
// stored locale rows and the document's locales.
func TestPublicStoreReadsThePublishedProjectionIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	contents, err := contentblock.NewGeneratedStore(unusedFileReuse{})
	require.NoError(t, err)
	units, err := pagepublicpostgres.NewUnits(db, contents, unusedShareLinks{})
	require.NoError(t, err)
	h := mutationtest.PostgresHarness(db, units)
	// The public read share-locks its rows, so it reads in a snapshot that
	// may lock.
	read := func(work func(ctx context.Context, store pagepublic.Store) error) {
		t.Helper()
		require.NoError(t, units.ReadLocked(t.Context(), func(store pagepublic.Store) error { return work(t.Context(), store) }))
	}
	read(func(ctx context.Context, store pagepublic.Store) error {
		homepage, err := store.HomepagePageID(ctx)
		require.NoError(t, err)
		require.Empty(t, homepage)
		_, err = store.FindPage(ctx, page.ByID, uuid.NewString())
		require.ErrorIs(t, err, page.ErrPageNotFound)
		return nil
	})

	pageID, slug := seedPublishedPage(t, db)
	require.NoError(t, db.Model(&model.SiteSettings{}).Where("id = ?", 1).Update("homepage_page_id", pageID).Error)
	err = mutationtest.Transact(t, h, func(ctx context.Context, store pagepublic.Store) error {
		homepage, err := store.HomepagePageID(ctx)
		require.NoError(t, err)
		require.Equal(t, pageID, homepage)
		for locator, value := range map[page.Locator]string{page.ByID: pageID, page.BySlug: slug} {
			found, err := store.FindPage(ctx, locator, value)
			require.NoError(t, err)
			require.Equal(t, pageID, found.ID)
			require.Equal(t, page.StatusPublished, found.Status)
		}
		root, err := store.DocumentRoot(ctx, pageID)
		require.NoError(t, err)
		require.NotNil(t, root.ContentDocumentID)
		source, exists, err := store.SourceMetadata(ctx, pageID)
		require.NoError(t, err)
		require.True(t, exists)
		require.Equal(t, "Source", *source.Title)

		settings := translation.DefaultRuntimeSettings()
		displayed, err := store.ResolveLocalization(ctx, pageID, "ko", map[string]struct{}{"ko": {}}, settings)
		require.NoError(t, err)
		require.Equal(t, "ko", displayed.DisplayedLocale)
		require.Equal(t, "번역", *displayed.Title)
		require.ElementsMatch(t, []string{"en", "ko"}, displayed.AvailableLocales)
		fallback, err := store.ResolveLocalization(ctx, pageID, "ko", map[string]struct{}{}, settings)
		require.NoError(t, err)
		require.Equal(t, "en", fallback.DisplayedLocale, "a locale without Blocks falls back to the source")
		require.Equal(t, []string{"en"}, fallback.AvailableLocales)
		return nil
	})
	require.NoError(t, err)
}

func seedPublishedPage(t *testing.T, db *gorm.DB) (string, string) {
	t.Helper()
	pageID, documentID, slug := uuid.NewString(), uuid.NewString(), "published-"+uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile) VALUES (?::uuid, 'page')`, documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO page (id, content_document_id, slug, status, published_at) VALUES (?::uuid, ?::uuid, ?, 'PAGE_STATUS_PUBLISHED', NOW())`, pageID, documentID, slug).Error)
	require.NoError(t, db.Exec(`INSERT INTO page_translation (entity_id, locale, title, created_at, updated_at) VALUES (?::uuid, 'en', 'Source', NOW(), NOW()), (?::uuid, 'ko', '번역', NOW(), NOW())`, pageID, pageID).Error)
	return pageID, slug
}
