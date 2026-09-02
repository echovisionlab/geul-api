package publiccontent

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/translation"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestSelectWithPolicyPrefersStoredRequestedLocale(t *testing.T) {
	t.Parallel()

	title := "Localized title"
	ogAssetID := "11111111-1111-4111-8111-111111111111"
	selection := selectWithPolicy("ko", "en", map[string]translationRow{
		"ko": {
			Locale: "ko", Title: &title, OgAssetID: &ogAssetID,
		},
	}, translation.DefaultRuntimeSettings())

	assert.Equal(t, "ko", selection.DisplayedLocale)
	assert.False(t, selection.IsOriginal)
	assert.False(t, selection.IsFallback)
	assert.Equal(t, &title, selection.Title)
	assert.Equal(t, &ogAssetID, selection.OgAssetID)
}

func TestSelectWithPolicyPreservesExplicitEmptyTargetField(t *testing.T) {
	t.Parallel()

	blank := "  "
	sourceTitle := "Source title"
	sourceBody := "Source body"
	selection := selectWithPolicy("ko", "fr", map[string]translationRow{
		"ko": {
			Locale: "ko", Title: &blank, ContentText: new("localized body"),
		},
		"fr": {
			Locale: "fr", Title: &sourceTitle, ContentText: &sourceBody,
		},
	}, translation.DefaultRuntimeSettings())

	assert.Equal(t, "ko", selection.DisplayedLocale)
	assert.False(t, selection.IsFallback)
	assert.False(t, selection.IsOriginal)
	assert.Equal(t, &blank, selection.Title)
	assert.Equal(t, "localized body", *selection.ContentText)
}

func TestSpecValidationRejectsTargetLifecycleAndProvenanceProjection(t *testing.T) {
	t.Parallel()

	for _, column := range []string{
		"status", "machine_generated", "provider", "model",
		"source_hash", "source_revision", "source_epoch", "published_at",
	} {
		t.Run(column, func(t *testing.T) {
			t.Parallel()
			require.Error(t, validateSpec(Spec{
				EntityType: "artist", TableName: "artist_translation",
				SelectClause: "locale, title, " + column,
			}))
		})
	}
}

func TestSelectWithPolicyFillsOnlyMissingTargetFieldsFromSource(t *testing.T) {
	t.Parallel()

	sourceTitle := "Source title"
	sourceBody := "Source body"
	targetTitle := "Target title"
	selection := selectWithPolicy("ko", "fr", map[string]translationRow{
		"ko": {Locale: "ko", Title: &targetTitle},
		"fr": {Locale: "fr", Title: &sourceTitle, ContentText: &sourceBody},
	}, translation.DefaultRuntimeSettings())

	assert.Equal(t, "ko", selection.DisplayedLocale)
	assert.False(t, selection.IsOriginal)
	assert.True(t, selection.IsFallback)
	assert.Equal(t, &targetTitle, selection.Title)
	assert.Equal(t, &sourceBody, selection.ContentText)
	assert.Equal(t, openv1.LocalizationFallbackReason_LOCALIZATION_FALLBACK_REASON_SOURCE, selection.FallbackReason)
}

func TestToProtoLocalizationInfoPreservesAvailableLocales(t *testing.T) {
	t.Parallel()

	info := ToProtoLocalizationInfo(Selection{
		RequestedLocale: "en", DisplayedLocale: "ko", SourceLocale: "ko",
		AvailableLocales: []string{"ko", "en"}, IsFallback: true, IsOriginal: true,
		FallbackReason: openv1.LocalizationFallbackReason_LOCALIZATION_FALLBACK_REASON_SOURCE,
	})

	require.NotNil(t, info)
	assert.Equal(t, []string{"ko", "en"}, info.AvailableLocales)
	assert.True(t, info.IsFallback)
	assert.True(t, info.IsOriginal)
}

func TestSpecValidationRejectsIncompleteProjection(t *testing.T) {
	t.Parallel()

	require.Error(t, validateSpec(Spec{EntityType: "artist", TableName: "artist_translation"}))
}

func TestRowsDoesNotFilterTargetBySourceIdentityOrLegacyStatus(t *testing.T) {
	t.Parallel()

	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "host=localhost user=geul dbname=geul sslmode=disable",
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	require.NoError(t, err)
	spec := Spec{
		EntityType: "series", TableName: "series_translation",
		SelectClause: "locale, title, NULL::text AS summary, NULL::jsonb AS content_json, content_html, content_text, og_asset_id",
	}
	query, err := Rows(t.Context(), db, spec)
	require.NoError(t, err)
	result := query.Select(spec.SelectClause).Where("entity_id = ?", "artist-1").Find(&[]translationRow{})
	require.NoError(t, result.Error)
	sql := result.Statement.SQL.String()
	assert.Contains(t, sql, `FROM "series_translation"`)
	assert.NotContains(t, sql, "translation_source_state")
	assert.NotContains(t, sql, "status")
	assert.NotContains(t, sql, "machine_generated")
}
