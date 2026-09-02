package sitesettings

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
)

func TestSiteSettingFormatValidation(t *testing.T) {
	service := &SiteSettingService{}

	for _, testCase := range []struct {
		name  string
		key   string
		value structured.Value
	}{
		{name: "legal email", key: "legal_email", value: "not-an-email"},
		{name: "support email", key: "support_email", value: "support@invalid"},
		{name: "privacy email", key: "privacy_email", value: "privacy @example.com"},
		{name: "primary color", key: "primary_color", value: "red"},
		{name: "analytics id", key: "google_analytics_id", value: "analytics-123"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			err := service.applySettingValue(&model.SiteSettings{}, testCase.key, testCase.value)
			require.Error(t, err)
		})
	}
}

func TestSiteSettingFormatValidationAcceptsSupportedValues(t *testing.T) {
	service := &SiteSettingService{}
	settings := &model.SiteSettings{}

	require.NoError(t, service.applySettingValue(settings, "legal_email", "legal@example.com"))
	require.NoError(t, service.applySettingValue(settings, "support_email", ""))
	require.NoError(t, service.applySettingValue(settings, "primary_color", "#b02d23"))
	require.NoError(t, service.applySettingValue(settings, "google_analytics_id", "G-ABC123"))
	require.NoError(t, service.applySettingValue(settings, "google_analytics_id", nil))
}
