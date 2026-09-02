package work

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestWorkAIDocumentSourceMetadataPreservesExplicitEmptySummary(t *testing.T) {
	db := newWorkAIDocumentMetadataDB(t)
	workID := uuid.NewString()
	seedWorkAIDocumentSource(t, db, workID, "en", "Source")
	empty := ""
	now := time.Date(2026, 8, 23, 1, 2, 3, 0, time.UTC)

	effect, err := applyWorkAIDocumentMetadata(
		context.Background(), db, workID, "en", true,
		AIDocumentMetadataPatch{SetSummary: true, Summary: &empty},
		now,
	)
	require.NoError(t, err)
	require.True(t, effect.Changed)
	require.True(t, effect.AffectsTranslationSource)
	require.Equal(t, []string{"en"}, effect.ChangedLocales)
}

func TestWorkAIDocumentIdentityRequiresExactCanonicalLocale(t *testing.T) {
	workID := uuid.NewString()
	require.NoError(t, validateWorkAIDocumentIdentity(workID, "en"))
	require.Error(t, validateWorkAIDocumentIdentity(workID, "EN"))
	require.Error(t, validateWorkAIDocumentIdentity(workID, " en"))
}

func TestWorkAIDocumentMetadataRejectsPresenceRaceAndEmptySourceTitle(t *testing.T) {
	db := newWorkAIDocumentMetadataDB(t)
	workID := uuid.NewString()
	seedWorkAIDocumentSource(t, db, workID, "en", "Source")
	empty := ""

	_, err := applyWorkAIDocumentMetadata(
		context.Background(), db, workID, "en", true,
		AIDocumentMetadataPatch{SetTitle: true, Title: &empty}, time.Now().UTC(),
	)
	require.ErrorContains(t, err, "source title cannot be empty")

	_, err = applyWorkAIDocumentMetadata(
		context.Background(), db, workID, "ko", true,
		AIDocumentMetadataPatch{SetSummary: true, Summary: &empty}, time.Now().UTC(),
	)
	require.ErrorContains(t, err, "current source locale")
}

func newWorkAIDocumentMetadataDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE work (
		id TEXT PRIMARY KEY,
		source_locale TEXT NOT NULL
	)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE work_translation (
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

func seedWorkAIDocumentSource(t *testing.T, db *gorm.DB, workID, locale, title string) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, db.Exec(`INSERT INTO work (id, source_locale) VALUES (?, ?)`, workID, locale).Error)
	require.NoError(t, db.Exec(`INSERT INTO work_translation (
		entity_id, locale, title, summary, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?)`, workID, locale, title, sql.NullString{}, now, now).Error)
}
