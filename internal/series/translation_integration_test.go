//go:build integration

package series

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTranslationPersistenceAndSourceProjectionIntegration(t *testing.T) {
	postgres := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true})
	ctx := t.Context()
	seriesID := "11111111-1111-4111-8111-111111111111"
	now := time.Date(2026, time.August, 20, 12, 0, 0, 0, time.UTC)
	require.NoError(t, postgres.DB.WithContext(ctx).Exec(
		`INSERT INTO translation_locale (code, display_name, dir, sort_order)
		 VALUES ('en', 'English', 'ltr', 1), ('ko', 'Korean', 'ltr', 2)
		 ON CONFLICT (code) DO NOTHING`,
	).Error)
	contentDocumentID := "22222222-2222-4222-8222-222222222222"
	contentDocumentRevision := "33333333-3333-4333-8333-333333333333"
	require.NoError(t, postgres.DB.WithContext(ctx).Exec(
		`INSERT INTO content_document (id, profile, revision, created_at, updated_at)
		 VALUES (?, 'compact', ?, ?, ?)`, contentDocumentID, contentDocumentRevision, now, now,
	).Error)
	require.NoError(t, postgres.DB.WithContext(ctx).Exec(
		`INSERT INTO series (id, slug, source_locale, content_document_id)
		 VALUES (?, 'series-translation', 'en', ?)`, seriesID, contentDocumentID,
	).Error)
	require.NoError(t, postgres.DB.WithContext(ctx).Exec(
		`INSERT INTO series_translation (
			entity_id, locale, title, summary
		) VALUES (?, 'en', 'Source title', 'Source summary')`, seriesID,
	).Error)

	document, err := LoadRequiredSourceLocaleDocument(ctx, postgres.DB, seriesID)
	require.NoError(t, err)
	require.NotNil(t, document.Title)
	assert.Equal(t, "Source title", *document.Title)
	require.NotNil(t, document.ContentText)
	assert.Equal(t, "Source summary", *document.ContentText)
	translationDocument, err := LoadTranslationSourceDocument(ctx, postgres.DB, seriesID)
	require.NoError(t, err)
	assert.Equal(t, "en", translationDocument.SourceLocale)
	assert.Equal(t, contentDocumentRevision, translationDocument.ContentDocumentRevision)
	targetTitle := "대상 제목"
	targetSummary := "대상 요약"
	require.NoError(t, UpsertTranslationEntry(ctx, postgres.DB, seriesID, "ko", translation.EntryWrite{
		Title: &targetTitle, Summary: &targetSummary, ContentText: &targetSummary,
		Now: now.Add(time.Minute),
	}))
	var target struct {
		Title   string `gorm:"column:title"`
		Summary string `gorm:"column:summary"`
	}
	require.NoError(t, postgres.DB.WithContext(ctx).Raw(
		`SELECT title, summary
		 FROM series_translation WHERE entity_id = ? AND locale = 'ko'`,
		seriesID,
	).Scan(&target).Error)
	assert.Equal(t, targetTitle, target.Title)
	assert.Equal(t, targetSummary, target.Summary)

	updatedSourceTitle := "Updated source title"
	updatedSourceSummary := "Updated source summary"
	require.NoError(t, UpsertTranslationEntry(ctx, postgres.DB, seriesID, "en", translation.EntryWrite{
		Title: &updatedSourceTitle, Summary: &updatedSourceSummary, ContentText: &updatedSourceSummary,
		Now: now.Add(2 * time.Minute),
	}))
}
