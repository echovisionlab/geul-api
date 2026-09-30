//go:build integration

package campaign

import (
	"context"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestCampaignDeliveryRecipientClaimFencesConcurrentAndExpiredWorkersIntegration(t *testing.T) {
	db := newCampaignConcurrentIntegrationDB(t)
	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	ctx := auth.WithUser(context.Background(), admin.AuthUserInfo())
	contentBlocks := testutil.NewEmailContentBlockStore(t, stack.SpiceDBClient)
	var campaignID, runID, identityID string
	t.Cleanup(func() {
		cleanupConcurrentCampaignDeliveryFixture(t, db, runID, campaignID, identityID, "")
	})
	ensureBulkEmailAudienceKratosIdentityColumns(t, db)

	now := time.Now().UTC()
	campaignService := NewCampaignService(
		db,
		newCampaignRuntimeFixture(nil, nil),
		"",
		"",
		stack.SpiceDBClient,
		WithCampaignContentBlockStore(contentBlocks),
	)
	created, err := campaignService.CreateCampaign(ctx, connect.NewRequest(&managev1.CreateCampaignRequest{
		Name:         "Campaign delivery claim " + uuid.NewString(),
		Subject:      "Campaign delivery claim",
		SourceLocale: "en",
		Target:       campaignAllTarget(),
	}))
	require.NoError(t, err)
	campaignID = created.Msg.Campaign.Id
	publishCampaignSourceBlocksForIntegration(t, db, stack.SpiceDBClient, campaignID, email.StripHTML("<p>Claim test</p>"))
	require.NoError(t, db.Model(&model.Campaign{}).
		Where("id = ?", campaignID).
		Updates(structured.Fields{"status": managev1.CampaignStatus_CAMPAIGN_STATUS_SENDING.String()}).Error)
	var campaign model.Campaign
	require.NoError(t, db.First(&campaign, "id = ?", campaignID).Error)

	identityID = seedBulkEmailAudienceIdentity(t, db, "claim-"+uuid.NewString()+"@example.test", now)
	memberID := memberIDForCampaignLockTest(t, db, identityID)
	var run *model.CampaignDeliveryRun
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		ref, createErr := createCampaignDeliveryRun(
			ctx,
			tx,
			campaign,
			now,
			0,
			campaignAudienceTargets{},
			contentBlocks,
			nil,
			NewCampaignDeliveryRuntime(stack.SpiceDBClient, nil),
		)
		if createErr != nil {
			return createErr
		}
		var persisted model.CampaignDeliveryRun
		if err := tx.First(&persisted, "id = ?", ref.ID).Error; err != nil {
			return err
		}
		run = &persisted
		return tx.Model(&model.CampaignDeliveryRun{}).
			Where("id = ?", ref.ID).
			Updates(structured.Fields{"status": CampaignDeliveryRunStatusSending, "target_count": 1, "started_at": now}).Error
	}))
	require.NotNil(t, run)
	runID = run.ID
	recipient := model.CampaignDeliveryRecipient{
		ID:                   uuid.NewString(),
		RunID:                run.ID,
		RecipientEmail:       identityEmailForCampaignLockTest(t, db, identityID),
		IdentityID:           &identityID,
		MemberID:             &memberID,
		RecipientContextType: BulkEmailContextNewsletterSubscription,
		Status:               CampaignDeliveryRecipientStatusPending,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	recipient.NormalizedRecipientEmail = email.NormalizeAddressForDelivery(recipient.RecipientEmail)
	require.NoError(t, db.Create(&recipient).Error)

	claimOne, claimTwo := uuid.NewString(), uuid.NewString()
	start := make(chan struct{})
	type claimResult struct {
		id      string
		claimed bool
		err     error
	}
	results := make(chan claimResult, 2)
	var workers sync.WaitGroup
	workers.Add(2)
	for _, claimID := range []string{claimOne, claimTwo} {
		claimID := claimID
		go func() {
			defer workers.Done()
			<-start
			claimed, claimErr := ClaimCampaignDeliveryRecipient(ctx, db, recipient.ID, claimID)
			results <- claimResult{id: claimID, claimed: claimed, err: claimErr}
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	winner := ""
	winners := 0
	for result := range results {
		require.NoError(t, result.err)
		if result.claimed {
			winner = result.id
			winners++
		}
	}
	require.Equal(t, 1, winners)
	pending, err := ListPendingCampaignDeliveryRecipients(ctx, db, run.ID, "", 10)
	require.NoError(t, err)
	require.Empty(t, pending, "an unexpired claim must keep the recipient out of pending dispatch")
	require.ErrorIs(t, MarkCampaignDeliveryRecipientResultWithAudit(
		ctx, db, nil, recipient.ID, CampaignDeliveryRecipientStatusSent, "provider-message", "", nil,
	), ErrCampaignDeliveryRecipientClaimed)

	reclaimedID := uuid.NewString()
	require.NoError(t, db.Model(&model.CampaignDeliveryRecipient{}).
		Where("id = ?", recipient.ID).
		Update("delivery_claim_expires_at", time.Now().UTC().Add(-time.Second)).Error)
	claimed, err := ClaimCampaignDeliveryRecipient(ctx, db, recipient.ID, reclaimedID)
	require.NoError(t, err)
	require.True(t, claimed, "an expired claim must be reclaimable")
	require.NoError(t, ReleaseCampaignDeliveryRecipientClaim(ctx, db, recipient.ID, winner))
	require.ErrorIs(t, MarkClaimedCampaignDeliveryRecipientResultWithAudit(
		ctx, db, nil, recipient.ID, winner,
		CampaignDeliveryRecipientStatusPermanentFailed, "", "stale_worker", nil,
	), ErrCampaignDeliveryClaimLost)

	require.NoError(t, MarkClaimedCampaignDeliveryRecipientResultWithAudit(
		ctx, db, nil, recipient.ID, reclaimedID,
		CampaignDeliveryRecipientStatusSent, "provider-message", "", nil,
	))
	var stored model.CampaignDeliveryRecipient
	require.NoError(t, db.First(&stored, "id = ?", recipient.ID).Error)
	require.Equal(t, CampaignDeliveryRecipientStatusSent, stored.Status)
	require.Nil(t, stored.DeliveryClaimID)
	require.Nil(t, stored.DeliveryClaimExpiresAt)
}
