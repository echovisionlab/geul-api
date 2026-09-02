package campaign

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNextSESProviderRecipientStatusIsMonotonicAndIdempotent(t *testing.T) {
	tests := []struct {
		name        string
		current     string
		outcome     SESProviderOutcome
		want        string
		wantApplied bool
	}{
		{"accepted to delivered", CampaignDeliveryRecipientStatusSent, SESProviderOutcomeDelivered, CampaignDeliveryRecipientStatusDelivered, true},
		{"delivery duplicate", CampaignDeliveryRecipientStatusDelivered, SESProviderOutcomeDelivered, CampaignDeliveryRecipientStatusDelivered, false},
		{"delivered to bounced", CampaignDeliveryRecipientStatusDelivered, SESProviderOutcomeBounced, CampaignDeliveryRecipientStatusBounced, true},
		{"bounce duplicate", CampaignDeliveryRecipientStatusBounced, SESProviderOutcomeBounced, CampaignDeliveryRecipientStatusBounced, false},
		{"bounced to complained", CampaignDeliveryRecipientStatusBounced, SESProviderOutcomeComplained, CampaignDeliveryRecipientStatusComplained, true},
		{"late delivery cannot overwrite complaint", CampaignDeliveryRecipientStatusComplained, SESProviderOutcomeDelivered, CampaignDeliveryRecipientStatusComplained, false},
		{"late bounce cannot overwrite complaint", CampaignDeliveryRecipientStatusComplained, SESProviderOutcomeBounced, CampaignDeliveryRecipientStatusComplained, false},
		{"pending cannot receive callback", CampaignDeliveryRecipientStatusPending, SESProviderOutcomeDelivered, CampaignDeliveryRecipientStatusPending, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, applied := nextSESProviderRecipientStatus(tt.current, tt.outcome)
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.wantApplied, applied)
		})
	}
}
