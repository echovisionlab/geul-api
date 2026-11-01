package sitesettings

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
)

func TestSiteSettingFormatValidation(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		key   string
		value structured.Value
	}{
		{name: "legal email", key: "legal_email", value: "not-an-email"},
		{name: "contact email", key: "contact_email", value: "contact@invalid"},
		{name: "primary color", key: "primary_color", value: "red"},
		{name: "analytics id", key: "google_analytics_id", value: "analytics-123"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			err := applySettingValue(&model.SiteSettings{}, testCase.key, testCase.value)
			require.Error(t, err)
		})
	}
}

func TestContactSettingsUseCanonicalStorageAndPreserveLegalAddress(t *testing.T) {
	settings := &model.SiteSettings{LegalEmail: "legal@example.test"}
	before := *settings
	require.NoError(t, applySettingValue(settings, "contact_email", "contact@example.test"))
	require.Equal(t, "contact@example.test", settings.ContactEmail)
	require.Equal(t, before.LegalEmail, settings.LegalEmail)
	require.Equal(t, []string{"contact_email"}, changedSiteSettingKeys(&before, settings, []string{"contact_email", "contact_email"}))
	value, ok := settingValue(settings, "contact_email")
	require.True(t, ok)
	require.Equal(t, "contact@example.test", value)
	require.NoError(t, applySettingValue(settings, "contact_email", nil))
	require.Empty(t, settings.ContactEmail)
	require.Equal(t, before.LegalEmail, settings.LegalEmail)
}

func TestContactSettingsRejectLegacyKeysBeforeBatchMutation(t *testing.T) {
	for _, key := range []string{"support_email", "privacy_email"} {
		settings := &model.SiteSettings{ContactEmail: "original@example.test"}
		require.Error(t, applySettingValue(settings, key, "contact@example.test"))
		_, known := settingValue(settings, key)
		require.False(t, known)
		require.NotContains(t, defaultSettings, key)
		requested := []Setting{
			{Key: "contact_email", Value: "contact@example.test"},
			{Key: key, Value: "contact@example.test"},
		}
		require.Error(t, applyRequestedSiteSettings(settings, requested))
		require.Equal(t, "original@example.test", settings.ContactEmail)
	}
}

func TestSiteSettingFormatValidationAcceptsSupportedValues(t *testing.T) {
	settings := &model.SiteSettings{}

	require.NoError(t, applySettingValue(settings, "legal_email", "legal@example.com"))
	require.NoError(t, applySettingValue(settings, "contact_email", ""))
	require.NoError(t, applySettingValue(settings, "primary_color", "#b02d23"))
	require.NoError(t, applySettingValue(settings, "google_analytics_id", "G-ABC123"))
	require.NoError(t, applySettingValue(settings, "google_analytics_id", nil))
}
