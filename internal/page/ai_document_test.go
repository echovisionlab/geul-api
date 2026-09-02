package page

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestPageAIDocumentMetadataCreatesTargetAndPreservesExplicitEmpty(t *testing.T) {
	db := newPageAIDocumentMetadataDB(t)
	pageID := uuid.NewString()
	seedPageAIDocumentSource(t, db, pageID, "en", "Source")
	empty := ""

	effect, err := applyPageAIDocumentMetadata(
		context.Background(), db, pageID, "ko", false,
		AIDocumentMetadataPatch{EnsureLocale: true, SetTitle: true, Title: &empty, SetSummary: true, Summary: &empty},
		time.Now().UTC(),
	)
	require.NoError(t, err)
	require.True(t, effect.Changed)
	require.False(t, effect.AffectsTranslationSource)
	require.Equal(t, []string{"ko"}, effect.ChangedLocales)

	locale, exists, err := loadPageAIDocumentLocale(context.Background(), db, pageID, "ko", false)
	require.NoError(t, err)
	require.True(t, exists)
	require.NotNil(t, locale.Title)
	require.Empty(t, *locale.Title)
	require.NotNil(t, locale.Summary)
	require.Empty(t, *locale.Summary)
}

func TestPageAIDocumentMetadataRejectsPresenceRace(t *testing.T) {
	db := newPageAIDocumentMetadataDB(t)
	pageID := uuid.NewString()
	seedPageAIDocumentSource(t, db, pageID, "en", "Source")
	empty := ""

	_, err := applyPageAIDocumentMetadata(
		context.Background(), db, pageID, "ko", true,
		AIDocumentMetadataPatch{SetTitle: true, Title: &empty}, time.Now().UTC(),
	)
	require.ErrorContains(t, err, "presence changed")
}

func TestPageAIDocumentMetadataMissingTargetNoOpDoesNotCreateLocale(t *testing.T) {
	db := newPageAIDocumentMetadataDB(t)
	pageID := uuid.NewString()
	seedPageAIDocumentSource(t, db, pageID, "en", "Source")

	effect, err := applyPageAIDocumentMetadata(
		context.Background(), db, pageID, "ko", false,
		AIDocumentMetadataPatch{}, time.Now().UTC(),
	)
	require.NoError(t, err)
	require.False(t, effect.Changed)
	_, exists, err := loadPageAIDocumentLocale(context.Background(), db, pageID, "ko", false)
	require.NoError(t, err)
	require.False(t, exists)
}

func newPageAIDocumentMetadataDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE page (
		id TEXT PRIMARY KEY,
		source_locale TEXT NOT NULL
	)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE page_translation (
		entity_id TEXT NOT NULL,
		locale TEXT NOT NULL,
		title TEXT,
		summary TEXT,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		PRIMARY KEY (entity_id, locale)
	)`).Error)
	return db
}

func seedPageAIDocumentSource(t *testing.T, db *gorm.DB, pageID, locale, title string) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, db.Exec(`INSERT INTO page (id, source_locale) VALUES (?, ?)`, pageID, locale).Error)
	require.NoError(t, db.Exec(`INSERT INTO page_translation (
		entity_id, locale, title, summary, created_at, updated_at
	) VALUES (?, ?, ?, NULL, ?, ?)`, pageID, locale, title, now, now).Error)
}
