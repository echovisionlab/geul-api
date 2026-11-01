//go:build integration

package campaign_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/testutil/contenttest"

	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/campaign/campaigntest"
	campaignpostgres "github.com/echovisionlab/geul-api/internal/campaign/postgres"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"

	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	// Real concurrent workers must produce one lease owner, and a stale worker must not finalize it.
	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
)

func TestCampaignDeliveryRecipientClaimFencesConcurrentAndExpiredWorkersIntegration(t *testing.T) {
	t.Parallel()
	db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true, BootstrapKratosStub: true}).DB
	units := mutation.Postgres(db, func(tx *gorm.DB) campaign.Store {
		return campaignpostgres.NewStore(tx, campaignpostgres.Ports{ContentBlocks: contenttest.NewEmailContentBlockStore(t)})
	})
	h := campaigntest.Harness{Harness: mutationtest.PostgresHarness(db, units)}
	c := campaigntest.SeedCampaign(t, h)
	run := campaigntest.SeedRun(t, h, c, "sending")
	identityID := uuid.NewString()
	memberID := orytest.SeedPostIntegrationIdentity(t, db, identityID, "Claim worker "+identityID)
	r := model.CampaignDeliveryRecipient{RunID: run.ID, RecipientEmail: "claim@example.test", MemberID: &memberID, IdentityID: &identityID, RecipientContextType: campaign.BulkEmailContextAccountCurrent, Status: "pending"}
	require.NoError(t, db.Create(&r).Error)
	joined := func(tx *gorm.DB) campaign.Joined {
		return campaignpostgres.Bind(tx, campaignpostgres.Ports{}, &audittest.Recorder{})
	}
	type result struct {
		id      string
		claimed bool
		err     error
	}
	start := make(chan struct{})
	done := make(chan result, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			id := uuid.NewString()
			claimed := false
			err := db.Transaction(func(tx *gorm.DB) error {
				var err error
				claimed, err = campaign.ClaimCampaignDeliveryRecipient(t.Context(), joined(tx), r.ID, id)
				return err
			})
			done <- result{id, claimed, err}
		}()
	}
	close(start)
	wg.Wait()
	close(done)
	winner := ""
	n := 0
	for got := range done {
		require.NoError(t, got.err)
		if got.claimed {
			n++
			winner = got.id
		}
	}
	require.Equal(t, 1, n)
	pending, err := campaign.ListPendingCampaignDeliveryRecipients(t.Context(), joined(db), run.ID, "", 10)
	require.NoError(t, err)
	require.Empty(t, pending)
	require.NoError(t, db.Model(&model.CampaignDeliveryRecipient{}).Where("id = ?", r.ID).Update("delivery_claim_expires_at", time.Now().UTC().Add(-time.Second)).Error)
	next := uuid.NewString()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		claimed, err := campaign.ClaimCampaignDeliveryRecipient(t.Context(), joined(tx), r.ID, next)
		require.True(t, claimed)
		return err
	}))
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return campaign.ReleaseCampaignDeliveryRecipientClaim(t.Context(), joined(tx), r.ID, winner)
	}))
	require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error {
		return campaign.MarkClaimedCampaignDeliveryRecipientResultWithAudit(t.Context(), joined(tx), r.ID, winner, "permanent_failed", "", "stale_worker", nil)
	}), campaign.ErrCampaignDeliveryClaimLost)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return campaign.MarkClaimedCampaignDeliveryRecipientResultWithAudit(context.Background(), joined(tx), r.ID, next, "sent", "provider-message", "", nil)
	}))
	var stored model.CampaignDeliveryRecipient
	require.NoError(t, db.First(&stored, "id = ?", r.ID).Error)
	require.Equal(t, "sent", stored.Status)
	require.Nil(t, stored.DeliveryClaimID)
	require.Nil(t, stored.DeliveryClaimExpiresAt)
}
