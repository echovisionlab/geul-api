//go:build integration

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
	db := newLocalizationIntegrationDB(t)
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
	db := newLocalizationIntegrationDB(t)
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
	db := newLocalizationIntegrationDB(t)
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

func newLocalizationIntegrationDB(t *testing.T) *gorm.DB {
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
