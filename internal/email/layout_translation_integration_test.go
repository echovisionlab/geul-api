//go:build integration

package email

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLayoutTranslationPersistenceIntegration(t *testing.T) {
	postgres := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true})
	ctx := t.Context()
	db := postgres.DB
	layoutID := "22222222-2222-4222-8222-222222222222"
	documentID := "33333333-3333-4333-8333-333333333333"
	now := time.Date(2026, time.August, 20, 14, 0, 0, 0, time.UTC)
	require.NoError(t, db.WithContext(ctx).Exec(
		`INSERT INTO translation_locale (code, display_name, dir, sort_order)
		 VALUES ('en', 'English', 'ltr', 1), ('ko', 'Korean', 'ltr', 2)
		 ON CONFLICT (code) DO NOTHING`,
	).Error)
	require.NoError(t, db.WithContext(ctx).Exec(
		`INSERT INTO content_document (id, profile) VALUES (?, 'compact')`,
		documentID,
	).Error)
	require.NoError(t, db.WithContext(ctx).Exec(
		`INSERT INTO email_layout (id, key, name, source_locale, content_document_id)
		 VALUES (?, 'translation-layout', 'Translation Layout', 'en', ?)`,
		layoutID,
		documentID,
	).Error)
	sourceHTML := `<main>{{content}}</main><footer>Source footer</footer>`
	require.NoError(t, SaveLayoutSourceLocaleDocument(
		ctx,
		db,
		layoutID,
		"en",
		LayoutTranslationDocument{ContentHTML: &sourceHTML},
		now,
	))

	resolvedLocale, found, err := ResolveLayoutTranslationSourceLocale(ctx, db, layoutID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "en", resolvedLocale)
	source, err := LoadCanonicalLayoutTranslationDocument(ctx, db, layoutID, "en")
	require.NoError(t, err)
	require.NotNil(t, source.ContentText)
	assert.Contains(t, *source.ContentText, "Source footer")

	require.NoError(t, SeedLayoutTranslationEntryFromSource(
		ctx, db, layoutID, "ko", "en", now.Add(time.Minute),
	))
	seeded, err := LoadLayoutTranslationEntry(ctx, db, layoutID, "ko")
	require.NoError(t, err)
	require.NotNil(t, seeded)
	require.NotNil(t, seeded.ContentHTML)
	assert.Contains(t, *seeded.ContentHTML, "Source footer")
	require.NotNil(t, seeded.ContentText)
	assert.Contains(t, *seeded.ContentText, "Source footer")

	require.NotNil(t, source.ContentHTML)
	units, err := ExtractLayoutContentUnits(*source.ContentHTML)
	require.NoError(t, err)
	var footerHandle string
	for _, unit := range units {
		if unit.SourceValue == "Source footer" {
			footerHandle = unit.Handle
			break
		}
	}
	require.NotEmpty(t, footerHandle)
	targetHTML, targetText, err := ApplyLayoutLocaleValues(*source.ContentHTML, map[string]string{
		footerHandle: "Target footer",
	})
	require.NoError(t, err)
	require.NoError(t, UpsertLayoutTranslationEntry(ctx, db, layoutID, "ko", translation.EntryWrite{
		ContentHTML: targetHTML, ContentText: targetText,
		Now: now.Add(2 * time.Minute),
	}))

	recaptured, err := LoadLayoutTranslationEntry(ctx, db, layoutID, "ko")
	require.NoError(t, err)
	require.NotNil(t, recaptured)
	require.NotNil(t, recaptured.ContentHTML)
	assert.Equal(t, *targetHTML, *recaptured.ContentHTML)
	require.NotNil(t, recaptured.ContentText)
	assert.Equal(t, *targetText, *recaptured.ContentText)

	editedSourceHTML, _, err := ApplyLayoutSourceValues(*source.ContentHTML, map[string]string{
		footerHandle: "Edited source footer",
	})
	require.NoError(t, err)
	require.NoError(t, SaveLayoutSourceLocaleDocument(
		ctx, db, layoutID, "en",
		LayoutTranslationDocument{ContentHTML: editedSourceHTML},
		now.Add(3*time.Minute),
	))
	recapturedAfterSourceEdit, err := LoadLayoutTranslationEntry(ctx, db, layoutID, "ko")
	require.NoError(t, err)
	require.Equal(t, recaptured.ContentHTML, recapturedAfterSourceEdit.ContentHTML)
	localeSnapshots, err := LoadLayoutLocaleSnapshots(ctx, db, layoutID)
	require.NoError(t, err)
	require.Len(t, localeSnapshots, 2)
	assert.Equal(t, "en", localeSnapshots[0].Locale)
	assert.True(t, localeSnapshots[0].IsSourceLocale)
	assert.Equal(t, "ko", localeSnapshots[1].Locale)
	assert.False(t, localeSnapshots[1].IsSourceLocale)
	require.NotNil(t, localeSnapshots[1].ContentHTML)
	assert.Contains(t, *localeSnapshots[1].ContentHTML, "Target footer")
	assert.NotContains(t, *localeSnapshots[1].ContentHTML, "geul-unit:")

	providerSource, err := LoadLayoutTranslationSourceDocument(ctx, db, layoutID, "en")
	require.NoError(t, err)
	require.NotNil(t, providerSource.ContentHTML)
	assert.Contains(t, *providerSource.ContentHTML, "Edited source footer")
}
