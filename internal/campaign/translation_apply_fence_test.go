package campaign_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/campaign/campaigntest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCampaignTranslationJobApplyFenceUsesRootExistenceOnly(t *testing.T) {
	for _, status := range []campaign.CampaignStatus{campaign.CampaignStatusDraft, campaign.CampaignStatusScheduled, campaign.CampaignStatusSending, campaign.CampaignStatusSent, campaign.CampaignStatusFailed} {
		t.Run(string(status), func(t *testing.T) {
			store, _ := campaigntest.New()
			row := store.PutCampaign(campaign.Campaign{Status: status})
			documentID := uuid.MustParse(*row.ContentDocumentID)
			domain, err := campaign.TranslationJobFence(store, row.ID)(t.Context(), documentID)
			require.NoError(t, err)
			require.Equal(t, "en", domain.SourceLocale)
			if status != campaign.CampaignStatusDraft {
				_, err = campaign.EditorContentFence(store, row.ID)(t.Context(), documentID)
				require.Equal(t, connect.CodeFailedPrecondition, errs.Wrap(err).Code())
			}
			_, err = campaign.TranslationJobFence(store, uuid.NewString())(t.Context(), documentID)
			require.Equal(t, connect.CodeNotFound, errs.Wrap(err).Code())
		})
	}
}
