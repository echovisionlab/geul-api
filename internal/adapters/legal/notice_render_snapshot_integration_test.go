//go:build integration

package legal

import (
	"encoding/json"
	"testing"

	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestEmailTemplateRenderSnapshotUsesExactSourceWithoutLegacySourceFlag(t *testing.T) {
	postgres := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true})

	var template model.EmailTemplate
	require.NoError(t, postgres.DB.Where("event_key = ?", "privacy_effective").Take(&template).Error)
	snapshot, err := loadNoticeRenderSnapshot(t.Context(), postgres.DB, template, "privacy_effective")
	require.NoError(t, err)
	require.Equal(t, "en", snapshot.SourceLocale)
	require.NotEmpty(t, snapshot.Translations)
	require.Equal(t, snapshot.SourceLocale, snapshot.Translations[0].Locale)
	require.Equal(t, snapshot.Subject, snapshot.Translations[0].Subject)
	require.Equal(t, snapshot.ContentHTML, snapshot.Translations[0].ContentHTML)

	encoded, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "is_source_locale")
	require.NotContains(t, string(encoded), "isSourceLocale")
	require.NotContains(t, string(encoded), `"status"`)
	require.NoError(t, campaign.ValidateCampaignDeliverySnapshot(snapshot))

	translations, err := json.Marshal(snapshot.Translations)
	require.NoError(t, err)
	var valid bool
	require.NoError(t, postgres.DB.Raw(
		`SELECT public.geul_render_translation_array_is_valid(?::jsonb, FALSE, ?)`,
		string(translations), snapshot.SourceLocale,
	).Scan(&valid).Error)
	require.True(t, valid)
}
