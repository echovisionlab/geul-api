package campaign

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEmailDeliveryCompletionDecisionMatrixForLegalNotice(t *testing.T) {
	tests := []struct {
		name      string
		counts    campaignDeliveryCompletionCounts
		target    int
		complete  bool
		runStatus string
	}{
		{
			name:      "zero target is skipped",
			target:    0,
			complete:  true,
			runStatus: CampaignDeliveryRunStatusSkipped,
		},
		{
			name: "sent recipient completes as sent",
			counts: campaignDeliveryCompletionCounts{
				Total: 1,
				Sent:  1,
			},
			target:    1,
			complete:  true,
			runStatus: CampaignDeliveryRunStatusSent,
		},
		{
			name: "zero sent blocked target fails",
			counts: campaignDeliveryCompletionCounts{
				Total:   1,
				Blocked: 1,
			},
			target:    1,
			complete:  true,
			runStatus: CampaignDeliveryRunStatusFailed,
		},
		{
			name: "pending target remains open",
			counts: campaignDeliveryCompletionCounts{
				Total:   1,
				Pending: 1,
			},
			target:   1,
			complete: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := decideEmailDeliveryCompletion(test.counts, test.target, EmailDeliveryRunKindLegalNotice)
			require.Equal(t, test.complete, got.Complete)
			require.Equal(t, test.runStatus, got.RunStatus)
			require.Empty(t, got.CampaignStatus)
		})
	}
}
