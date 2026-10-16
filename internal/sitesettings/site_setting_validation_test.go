package sitesettings

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestSiteSettingFormatValidation(t *testing.T) {
	service := &SiteSettingService{}

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
			err := service.applySettingValue(&model.SiteSettings{}, testCase.key, testCase.value)
			require.Error(t, err)
		})
	}
}

func TestContactSettingsUseCanonicalStorageAndPreserveLegalAddress(t *testing.T) {
	service := &SiteSettingService{}
	settings := &model.SiteSettings{LegalEmail: "legal@example.test"}
	before := *settings
	require.NoError(t, service.applySettingValue(settings, "contact_email", "contact@example.test"))
	require.Equal(t, "contact@example.test", settings.ContactEmail)
	require.Equal(t, before.LegalEmail, settings.LegalEmail)
	require.Equal(t, []string{"contact_email"}, service.changedSiteSettingKeys(&before, settings, []string{"contact_email", "contact_email"}))
	value, ok := settingValue(settings, "contact_email")
	require.True(t, ok)
	require.Equal(t, "contact@example.test", value)
	require.NoError(t, service.applySettingValue(settings, "contact_email", nil))
	require.Empty(t, settings.ContactEmail)
	require.Equal(t, before.LegalEmail, settings.LegalEmail)
}

func TestContactSettingsRejectLegacyKeysBeforeBatchMutation(t *testing.T) {
	service := &SiteSettingService{}
	for _, key := range []string{"support_email", "privacy_email"} {
		settings := &model.SiteSettings{ContactEmail: "original@example.test"}
		require.Error(t, service.applySettingValue(settings, key, "contact@example.test"))
		_, known := settingValue(settings, key)
		require.False(t, known)
		require.NotContains(t, defaultSettings, key)
		requested := []*managev1.SiteSetting{
			{Key: "contact_email", Value: structpb.NewStringValue("contact@example.test")},
			{Key: key, Value: structpb.NewStringValue("contact@example.test")},
		}
		require.Error(t, service.applyRequestedSiteSettings(settings, requested))
		require.Equal(t, "original@example.test", settings.ContactEmail)
	}
}

func TestSiteSettingFormatValidationAcceptsSupportedValues(t *testing.T) {
	service := &SiteSettingService{}
	settings := &model.SiteSettings{}

	require.NoError(t, service.applySettingValue(settings, "legal_email", "legal@example.com"))
	require.NoError(t, service.applySettingValue(settings, "contact_email", ""))
	require.NoError(t, service.applySettingValue(settings, "primary_color", "#b02d23"))
	require.NoError(t, service.applySettingValue(settings, "google_analytics_id", "G-ABC123"))
	require.NoError(t, service.applySettingValue(settings, "google_analytics_id", nil))
}
