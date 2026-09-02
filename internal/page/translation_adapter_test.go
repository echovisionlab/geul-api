package page

import (
	"context"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestPageTranslationMetadataRejectsLegacyBodyAndTargetSourceMutation(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE page (
			id TEXT PRIMARY KEY,
			source_locale TEXT NOT NULL
		);
		CREATE TABLE page_translation (
			entity_id TEXT NOT NULL,
			locale TEXT NOT NULL,
			title TEXT,
			summary TEXT,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			og_asset_id TEXT,
			PRIMARY KEY (entity_id, locale)
		)
	`).Error)

	pageID := uuid.NewString()
	now := time.Unix(1_700_000_000, 0).UTC()
	require.NoError(t, db.Exec(
		`INSERT INTO page (id, source_locale) VALUES (?, 'en')`,
		pageID,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO page_translation (
			entity_id, locale, title, summary, created_at, updated_at
		) VALUES (?, 'en', 'Source title', 'Source summary', ?, ?)`,
		pageID,
		now,
		now,
	).Error)

	require.NoError(t, ValidateSourceLocaleChanges(
		context.Background(),
		db,
		pageID,
		"en",
		[]string{"en"},
	))
	require.Error(t, ValidateSourceLocaleChanges(
		context.Background(),
		db,
		pageID,
		"en",
		[]string{"ko"},
	))

	err = UpsertTranslationMetadataEntry(
		context.Background(),
		db,
		pageID,
		"de",
		translation.EntryWrite{ContentJSON: []byte(`{}`)},
	)
	require.Error(t, err)
}

func TestUpsertPageTranslationMetadataPersistsOnlyLocaleValues(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE page_translation (
		entity_id TEXT NOT NULL,
		locale TEXT NOT NULL,
		title TEXT,
		summary TEXT,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		PRIMARY KEY (entity_id, locale)
	)`).Error)

	pageID := uuid.NewString()
	title := "Titre traduit"
	explicitEmpty := ""
	now := time.Unix(1_700_000_100, 0).UTC()
	require.NoError(t, UpsertTranslationMetadataEntry(
		context.Background(), db, pageID, "fr",
		translation.EntryWrite{Title: &title, Summary: &explicitEmpty, Now: now},
	))

	var row struct {
		Title   *string `gorm:"column:title"`
		Summary *string `gorm:"column:summary"`
	}
	require.NoError(t, db.Table("page_translation").
		Where("entity_id = ? AND locale = ?", pageID, "fr").Take(&row).Error)
	require.Equal(t, title, *row.Title)
	require.NotNil(t, row.Summary)
	require.Empty(t, *row.Summary, "explicit empty is a stored target value")

	nextExplicitEmpty := ""
	require.NoError(t, UpsertTranslationMetadataEntry(
		context.Background(), db, pageID, "fr",
		translation.EntryWrite{Title: &nextExplicitEmpty, Summary: nil, Now: now.Add(time.Second)},
	))
	require.NoError(t, db.Table("page_translation").
		Where("entity_id = ? AND locale = ?", pageID, "fr").Take(&row).Error)
	require.NotNil(t, row.Title)
	require.Empty(t, *row.Title, "explicit empty must not be collapsed to missing")
	require.Nil(t, row.Summary, "nil is a missing target unit")
}
