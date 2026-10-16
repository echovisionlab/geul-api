package publiccontent

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestResolveWithPolicyUsesExistingTargetAndOnlyMissingFieldFallback(t *testing.T) {
	db := newLocalizationTestDB(t)
	entityID := uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO series (id, source_locale) VALUES (?, ?)`, entityID, "en").Error)
	sourceTitle, sourceBody := "Source title", "Source body"
	targetTitle := ""
	require.NoError(t, db.Exec(`INSERT INTO series_translation (entity_id, locale, title, content_text) VALUES (?, ?, ?, ?)`, entityID, "en", sourceTitle, sourceBody).Error)
	require.NoError(t, db.Exec(`INSERT INTO series_translation (entity_id, locale, title, content_text) VALUES (?, ?, ?, ?)`, entityID, "ko", targetTitle, nil).Error)

	spec := Spec{
		EntityType: "series", TableName: "series_translation",
		SelectClause: "locale, title, NULL AS summary, NULL AS content_json, NULL AS content_html, content_text, NULL AS og_asset_id",
	}
	selection, err := ResolveWithPolicy(t.Context(), db, spec, entityID, "ko", translation.DefaultRuntimeSettings())
	require.NoError(t, err)
	require.Equal(t, "ko", selection.DisplayedLocale)
	require.False(t, selection.IsOriginal)
	require.True(t, selection.IsFallback)
	require.NotNil(t, selection.Title)
	require.Empty(t, *selection.Title, "explicit empty target title must remain empty")
	require.Equal(t, &sourceBody, selection.ContentText)
	require.Equal(t, []string{"en", "ko"}, selection.AvailableLocales)
}

func TestDeletedTargetFallsBackAndRecreatedEmptyTargetIsImmediatelyVisible(t *testing.T) {
	db := newLocalizationTestDB(t)
	entityID := uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO series (id, source_locale) VALUES (?, ?)`, entityID, "en").Error)
	require.NoError(t, db.Exec(`INSERT INTO series_translation (entity_id, locale, title, content_text) VALUES (?, 'en', 'Source', 'Source body')`, entityID).Error)
	require.NoError(t, db.Exec(`INSERT INTO series_translation (entity_id, locale, title, content_text) VALUES (?, 'ko', 'Old target', NULL)`, entityID).Error)
	spec := Spec{
		EntityType: "series", TableName: "series_translation",
		SelectClause: "locale, title, NULL AS summary, NULL AS content_json, NULL AS content_html, content_text, NULL AS og_asset_id",
	}

	require.NoError(t, db.Exec(`DELETE FROM series_translation WHERE entity_id = ? AND locale = 'ko'`, entityID).Error)
	missing, err := ResolveWithPolicy(t.Context(), db, spec, entityID, "ko", translation.DefaultRuntimeSettings())
	require.NoError(t, err)
	require.Equal(t, "en", missing.DisplayedLocale)
	require.Equal(t, []string{"en"}, missing.AvailableLocales)
	require.Equal(t, "Source", *missing.Title)

	require.NoError(t, db.Exec(`INSERT INTO series_translation (entity_id, locale, title, content_text) VALUES (?, 'ko', '', NULL)`, entityID).Error)
	recreated, err := ResolveWithPolicy(t.Context(), db, spec, entityID, "ko", translation.DefaultRuntimeSettings())
	require.NoError(t, err)
	require.Equal(t, "ko", recreated.DisplayedLocale)
	require.Equal(t, []string{"en", "ko"}, recreated.AvailableLocales)
	require.NotNil(t, recreated.Title)
	require.Empty(t, *recreated.Title, "explicit empty recreated target must not fall back")
	require.Equal(t, "Source body", *recreated.ContentText, "only the missing target unit falls back")
}

func TestResolveBatchReportsEveryStoredTargetLocale(t *testing.T) {
	db := newLocalizationTestDB(t)
	entityID := uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO series (id, source_locale) VALUES (?, 'en')`, entityID).Error)
	require.NoError(t, db.Exec(`INSERT INTO series_translation (entity_id, locale, title) VALUES (?, 'en', 'Source'), (?, 'fr', ''), (?, 'ko', NULL)`, entityID, entityID, entityID).Error)
	spec := Spec{
		EntityType: "series", TableName: "series_translation",
		SelectClause: "locale, title, NULL AS summary, NULL AS content_json, NULL AS content_html, content_text, NULL AS og_asset_id",
	}

	selections, err := ResolveBatchWithPolicy(
		t.Context(), db, spec, []string{entityID}, "ja", translation.DefaultRuntimeSettings(),
	)
	require.NoError(t, err)
	require.Equal(t, []string{"en", "fr", "ko"}, selections[entityID].AvailableLocales)
	require.Equal(t, "en", selections[entityID].DisplayedLocale)
}

func newLocalizationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE series (
			id TEXT NOT NULL,
			source_locale TEXT NOT NULL
		);
		CREATE TABLE series_translation (
			entity_id TEXT NOT NULL,
			locale TEXT NOT NULL,
			title TEXT,
			content_text TEXT
		);
	`).Error)
	return db
}

func TestPublicSummaryResolvesTokensWithoutChangingStoredTemplates(t *testing.T) {
	db := newLocalizationTestDB(t)
	require.NoError(t, db.Exec(`ALTER TABLE series_translation ADD COLUMN summary TEXT`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE site_settings(id INTEGER PRIMARY KEY,site_title TEXT,company_name TEXT,legal_email TEXT,support_email TEXT,privacy_email TEXT)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO site_settings VALUES(1,'rawsector','{{site.title}}','','support@example.test','')`).Error)
	entityID := uuid.NewString()
	source := `About {{site.title}}: {{contact.supportEmail}} / {{site.companyName}}`
	require.NoError(t, db.Exec(`INSERT INTO series(id,source_locale) VALUES(?,'en')`, entityID).Error)
	require.NoError(t, db.Exec(`INSERT INTO series_translation(entity_id,locale,title,summary) VALUES(?,'en','Title',?)`, entityID, source).Error)
	spec := Spec{EntityType: "series", TableName: "series_translation", SelectClause: "locale, title, summary"}
	selected, err := ResolveWithPolicy(t.Context(), db, spec, entityID, "en", translation.DefaultRuntimeSettings())
	require.NoError(t, err)
	require.Equal(t, "About rawsector: support@example.test / {{site.title}}", *selected.Summary)
	batched, err := ResolveBatchWithPolicy(t.Context(), db, spec, []string{entityID}, "en", translation.DefaultRuntimeSettings())
	require.NoError(t, err)
	require.Equal(t, selected.Summary, batched[entityID].Summary)
	fallback, err := FallbackToSource(t.Context(), db, spec, entityID, selected)
	require.NoError(t, err)
	require.Equal(t, selected.Summary, fallback.Summary)
	var stored string
	require.NoError(t, db.Table("series_translation").Select("summary").Where("entity_id = ?", entityID).Scan(&stored).Error)
	require.Equal(t, source, stored)
}
