package public

import (
	"testing"

	pagedomain "github.com/echovisionlab/geul-api/internal/page"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestApplyLocalizedPageFieldsDoesNotUseLegacyBodyProjection(t *testing.T) {
	t.Parallel()

	document := &contentv1.LocalizedPageDocument{Locale: "en"}
	page := &openv1.Page{Document: document}
	localizedTitle := "Localized title"
	applyLocalizedPageFields(page, publiccontent.Selection{
		DisplayedLocale: "ko",
		SourceLocale:    "en",
		IsOriginal:      false,
		Title:           &localizedTitle,
	})

	assert.Equal(t, localizedTitle, page.Title)
	assert.Same(t, document, page.Document)
}

func TestResolvePageLocalizedMetadataDoesNotRequireLegacyBodyColumns(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:page-localized-metadata?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE page (
			id text PRIMARY KEY,
			source_locale text NOT NULL
		)
	`).Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE page_translation (
			entity_id text NOT NULL,
			locale text NOT NULL,
			title text,
			summary text,
			og_asset_id text,
			PRIMARY KEY (entity_id, locale)
		)
	`).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO page (id, source_locale)
		VALUES ('page-1', 'en'), ('page-2', 'fr')
	`).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO page_translation
			(entity_id, locale, title, summary)
		VALUES
			('page-1', 'en', 'Source', 'Source summary'),
			('page-1', 'ko', '번역', '번역 요약'),
			('page-2', 'fr', 'Source fr', 'Source fr summary'),
			('page-2', 'en', 'English', 'English summary'),
			('page-2', 'ko', '한국어', '한국어 요약')
	`).Error)
	sourceMetadata, err := pagedomain.LoadPageSourceLocaleMetadataForPublic(t.Context(), db, "page-1")
	require.NoError(t, err)
	require.NotNil(t, sourceMetadata)
	assert.Equal(t, "en", sourceMetadata.Locale)

	selection, err := resolvePageLocalizedMetadataSelection(
		t.Context(),
		db,
		"page-1",
		"ko",
		map[string]struct{}{"ko": {}},
		translation.DefaultRuntimeSettings(),
	)
	require.NoError(t, err)
	require.NotNil(t, selection.Title)
	require.NotNil(t, selection.Summary)
	assert.Equal(t, "번역", *selection.Title)
	assert.Equal(t, "번역 요약", *selection.Summary)
	assert.Equal(t, "ko", selection.DisplayedLocale)

	sourceFallback, err := resolvePageLocalizedMetadataSelection(
		t.Context(),
		db,
		"page-1",
		"ko",
		map[string]struct{}{},
		translation.DefaultRuntimeSettings(),
	)
	require.NoError(t, err)
	require.NotNil(t, sourceFallback.Title)
	assert.Equal(t, "Source", *sourceFallback.Title)
	assert.Equal(t, "en", sourceFallback.DisplayedLocale)
	assert.True(t, sourceFallback.IsOriginal)
	assert.NotContains(t, sourceFallback.AvailableLocales, "ko")

	englishFallback, err := resolvePageLocalizedMetadataSelection(
		t.Context(),
		db,
		"page-2",
		"ko",
		map[string]struct{}{"en": {}},
		translation.DefaultRuntimeSettings(),
	)
	require.NoError(t, err)
	require.NotNil(t, englishFallback.Title)
	assert.Equal(t, "Source fr", *englishFallback.Title)
	assert.Equal(t, "fr", englishFallback.DisplayedLocale)
	assert.True(t, englishFallback.IsFallback)
	assert.Contains(t, englishFallback.AvailableLocales, "en")
	assert.NotContains(t, englishFallback.AvailableLocales, "ko")

	frenchFallback, err := resolvePageLocalizedMetadataSelection(
		t.Context(),
		db,
		"page-2",
		"ko",
		map[string]struct{}{},
		translation.DefaultRuntimeSettings(),
	)
	require.NoError(t, err)
	require.NotNil(t, frenchFallback.Title)
	assert.Equal(t, "Source fr", *frenchFallback.Title)
	assert.Equal(t, "fr", frenchFallback.DisplayedLocale)
	assert.True(t, frenchFallback.IsOriginal)
}
