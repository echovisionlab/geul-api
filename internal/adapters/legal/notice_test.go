package legal

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/stretchr/testify/require"
)

func TestValidateLegalNoticeTemplateDataAcceptsExactEventShape(t *testing.T) {
	t.Parallel()

	require.NoError(t, campaign.ValidateLegalNoticeDeliveryTemplateData("terms_update", map[string]string{
		"policy_title": "Terms", "effective_date": "2026-09-01", "preview_url": "https://example.test/s/token",
	}))
	require.NoError(t, campaign.ValidateLegalNoticeDeliveryTemplateData("privacy_effective", map[string]string{
		"privacy_url": "https://example.test/privacy",
	}))
}

func TestValidateLegalNoticeTemplateDataRejectsMissingOrExtraFields(t *testing.T) {
	t.Parallel()

	require.Error(t, campaign.ValidateLegalNoticeDeliveryTemplateData("privacy_update", map[string]string{
		"policy_title": "Privacy", "effective_date": "2026-09-01",
	}))
	require.Error(t, campaign.ValidateLegalNoticeDeliveryTemplateData("terms_effective", map[string]string{
		"terms_url": "https://example.test/terms", "preview_url": "https://example.test/s/token",
	}))
}
