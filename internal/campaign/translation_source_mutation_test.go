package campaign_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/campaign/campaigntest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/stretchr/testify/require"
)

func TestRequireTranslationSourceMutableOwnsCampaignAndDeliveryFences(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status campaign.CampaignStatus
		run    string
		code   connect.Code
	}{{"draft", campaign.CampaignStatusDraft, "", 0}, {"scheduled", campaign.CampaignStatusScheduled, "", connect.CodeFailedPrecondition}, {"active run", campaign.CampaignStatusDraft, "scheduled", connect.CodeFailedPrecondition}, {"terminal run", campaign.CampaignStatusDraft, "sent", 0}, {"missing", "", "", connect.CodeNotFound}} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := campaigntest.New()
			id := "missing"
			if tc.status != "" {
				c := s.PutCampaign(campaign.Campaign{Status: tc.status})
				id = c.ID
				if tc.run != "" {
					s.PutRun(campaign.DeliveryRun{CampaignID: &id, Status: tc.run}, campaigntest.Target())
				}
			}
			err := campaign.RequireTranslationSourceMutable(t.Context(), campaign.Joined{Store: s}, id)
			if tc.code == 0 {
				require.NoError(t, err)
			} else {
				require.Equal(t, tc.code, errs.Wrap(err).Code())
			}
		})
	}
}
