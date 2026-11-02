package campaign_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/campaign/campaigntest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func deliveryFixture(h *harness) (campaign.Campaign, campaign.DeliveryRun, campaign.DeliveryRecipient) {
	c := h.store.PutCampaign(campaign.Campaign{Status: campaign.CampaignStatusSending})
	run := h.store.PutRun(campaign.DeliveryRun{RunKind: campaign.EmailDeliveryRunKindCampaign, CampaignID: &c.ID, Status: "sending", ScheduledAt: h.now, TargetCount: 1, DefinitionSealed: true, SnapshotSchemaVersion: 1, TargetQueryVersion: 2, TargetMode: campaign.CampaignDeliveryTargetModeAllUsers, TargetRecipientScope: "SUBSCRIBED_USERS"}, campaigntest.Target())
	r := h.store.PutRecipient(campaign.DeliveryRecipient{RunID: run.ID, RecipientEmail: "recipient@example.test", RecipientContextType: campaign.BulkEmailContextAccountCurrent})
	return c, run, r
}
func (h *harness) joined(store campaign.Store) campaign.Joined {
	return campaign.Joined{Store: store, Audit: h.audit}
}
func (h *harness) transaction(work func(context.Context, campaign.Joined) error) error {
	return h.units.Transaction(h.ctx, false, func(tx mutation.Tx[campaign.Store]) error { return work(h.ctx, h.joined(tx.Store())) })
}
func TestRecipientClaimsFenceExpiredAndStaleWorkers(t *testing.T) {
	h := newHarness(t)
	_, run, r := deliveryFixture(h)
	first, next := uuid.NewString(), uuid.NewString()
	require.NoError(t, h.transaction(func(ctx context.Context, j campaign.Joined) error {
		claimed, err := campaign.ClaimCampaignDeliveryRecipient(ctx, j, r.ID, first)
		require.True(t, claimed)
		return err
	}))
	require.NoError(t, h.transaction(func(ctx context.Context, j campaign.Joined) error {
		claimed, err := campaign.ClaimCampaignDeliveryRecipient(ctx, j, r.ID, next)
		require.False(t, claimed)
		return err
	}))
	pending, err := campaign.ListPendingCampaignDeliveryRecipients(h.ctx, h.joined(h.store), run.ID, "", 10)
	require.NoError(t, err)
	require.Empty(t, pending)
	require.ErrorIs(t, h.transaction(func(ctx context.Context, j campaign.Joined) error {
		return campaign.MarkCampaignDeliveryRecipientResultWithAudit(ctx, j, r.ID, "sent", "provider", "", nil)
	}), campaign.ErrCampaignDeliveryRecipientClaimed)
	stored, _ := h.store.Recipient(r.ID)
	expired := h.now.Add(-time.Hour)
	stored.DeliveryClaimExpiresAt = &expired
	h.store.PutRecipient(stored)
	require.NoError(t, h.transaction(func(ctx context.Context, j campaign.Joined) error {
		claimed, err := campaign.ClaimCampaignDeliveryRecipient(ctx, j, r.ID, next)
		require.True(t, claimed)
		return err
	}))
	require.NoError(t, h.transaction(func(ctx context.Context, j campaign.Joined) error {
		return campaign.ReleaseCampaignDeliveryRecipientClaim(ctx, j, r.ID, first)
	}))
	stored, _ = h.store.Recipient(r.ID)
	require.Equal(t, &next, stored.DeliveryClaimID)
	require.ErrorIs(t, h.transaction(func(ctx context.Context, j campaign.Joined) error {
		return campaign.MarkClaimedCampaignDeliveryRecipientResultWithAudit(ctx, j, r.ID, first, "permanent_failed", "", "stale_worker", nil)
	}), campaign.ErrCampaignDeliveryClaimLost)
	require.NoError(t, h.transaction(func(ctx context.Context, j campaign.Joined) error {
		return campaign.MarkClaimedCampaignDeliveryRecipientResultWithAudit(ctx, j, r.ID, next, "sent", "provider", "", nil)
	}))
	require.NoError(t, h.transaction(func(ctx context.Context, j campaign.Joined) error {
		return campaign.MarkClaimedCampaignDeliveryRecipientResultWithAudit(ctx, j, r.ID, first, "permanent_failed", "", "stale_worker", nil)
	}))
	stored, _ = h.store.Recipient(r.ID)
	require.Equal(t, "sent", stored.Status)
	require.Equal(t, "provider", *stored.ProviderMessageID)
	require.Nil(t, stored.DeliveryClaimID)
	require.Len(t, h.audit.Records(), 1)
}
func TestResultValidationTerminalAggregationAndAuditRollback(t *testing.T) {
	for _, tc := range []struct {
		name, status, provider string
		invalid                bool
	}{{"pending", "pending", "", true}, {"accepted without provider", "sent", "", true}, {"failed with provider", "blocked", "provider", true}, {"sent", "sent", "provider", false}, {"blocked", "blocked", "", false}, {"suppressed", "suppressed", "", false}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			c, run, r := deliveryFixture(h)
			err := h.transaction(func(ctx context.Context, j campaign.Joined) error {
				return campaign.MarkCampaignDeliveryRecipientResultWithAudit(ctx, j, r.ID, tc.status, tc.provider, "reason", nil)
			})
			if tc.invalid {
				require.Error(t, err)
				stored, _ := h.store.Recipient(r.ID)
				require.Equal(t, "pending", stored.Status)
				require.Empty(t, h.audit.Records())
				return
			}
			require.NoError(t, err)
			stored, _ := h.store.Run(run.ID)
			root, _ := h.store.Campaign(c.ID)
			want := "failed"
			wantRoot := campaign.CampaignStatusFailed
			if tc.status == "sent" {
				want = "sent"
				wantRoot = campaign.CampaignStatusSent
				require.Equal(t, 1, root.SentCount)
				require.NotNil(t, root.SentAt)
			}
			require.Equal(t, want, stored.Status)
			require.Equal(t, wantRoot, root.Status)
			require.NotNil(t, stored.CompletedAt)
			records := h.audit.Records()
			require.Len(t, records, 1)
			require.Equal(t, sharedtelemetry.ActorKindSystem, records[0].Kind)
			require.Equal(t, sharedtelemetry.AuditCampaignUpdated, records[0].Action)
		})
	}
	h := newHarness(t)
	c, run, r := deliveryFixture(h)
	want := errors.New("audit unavailable")
	h.audit.Err = want
	require.ErrorIs(t, h.transaction(func(ctx context.Context, j campaign.Joined) error {
		return campaign.MarkCampaignDeliveryRecipientResultWithAudit(ctx, j, r.ID, "sent", "provider", "", nil)
	}), want)
	stored, _ := h.store.Recipient(r.ID)
	require.Equal(t, "pending", stored.Status)
	storedRun, _ := h.store.Run(run.ID)
	require.Equal(t, run, storedRun)
	storedRoot, _ := h.store.Campaign(c.ID)
	require.Equal(t, c, storedRoot)
	require.Equal(t, mutation.StepRollback, h.lastStep())
}
func TestMaterializationIsIdempotentAndKeepsPendingUntilEveryResult(t *testing.T) {
	h := newHarness(t)
	c, run, r := deliveryFixture(h)
	h.store.Candidates = []campaign.DeliveryRecipient{{RecipientEmail: r.RecipientEmail}, {RecipientEmail: "second@example.test"}}
	for range 2 {
		require.NoError(t, h.transaction(func(ctx context.Context, j campaign.Joined) error {
			return campaign.MaterializeCampaignDeliveryRun(ctx, j, run.ID)
		}))
	}
	got, _ := h.store.Run(run.ID)
	require.Equal(t, 2, got.TargetCount)
	require.NoError(t, h.transaction(func(ctx context.Context, j campaign.Joined) error {
		return campaign.MarkCampaignDeliveryRecipientResultWithAudit(ctx, j, r.ID, "sent", "provider", "", nil)
	}))
	got, _ = h.store.Run(run.ID)
	require.Equal(t, "sending", got.Status)
	require.Equal(t, 1, got.SentCount)
	require.Empty(t, h.audit.Records())
	pending, err := campaign.ListPendingCampaignDeliveryRecipients(h.ctx, h.joined(h.store), run.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.NoError(t, h.transaction(func(ctx context.Context, j campaign.Joined) error {
		return campaign.MarkCampaignDeliveryRecipientResultWithAudit(ctx, j, pending[0].Recipient.ID, "skipped", "", "policy", nil)
	}))
	got, _ = h.store.Run(run.ID)
	require.Equal(t, "sent", got.Status)
	root, _ := h.store.Campaign(c.ID)
	require.Equal(t, 1, root.SentCount)
	require.Len(t, h.audit.Records(), 1)
}
func TestProviderOutcomesAreMonotoneAndPreserveAcceptance(t *testing.T) {
	h := newHarness(t)
	_, run, r := deliveryFixture(h)
	require.NoError(t, h.transaction(func(ctx context.Context, j campaign.Joined) error {
		return campaign.MarkCampaignDeliveryRecipientResultWithAudit(ctx, j, r.ID, "sent", "provider", "", nil)
	}))
	for _, outcome := range []campaign.SESProviderOutcome{campaign.SESProviderOutcomeDelivered, campaign.SESProviderOutcomeBounced, campaign.SESProviderOutcomeComplained, campaign.SESProviderOutcomeDelivered} {
		application, err := campaign.PrepareSESProviderOutcome(h.ctx, h.joined(h.store), "provider", outcome, h.now, "provider_reason")
		require.NoError(t, err)
		require.Equal(t, []string{run.ID}, application.RunIDs)
		for _, id := range application.RunIDs {
			var result campaign.SESProviderRunOutcome
			require.NoError(t, h.transaction(func(ctx context.Context, j campaign.Joined) error {
				var err error
				result, err = application.ApplyToRun(ctx, j, id)
				return err
			}))
			application.Record(result)
		}
		require.Equal(t, []string{r.RecipientEmail}, application.Result().MatchedRecipientEmails)
	}
	stored, _ := h.store.Recipient(r.ID)
	require.Equal(t, "complained", stored.Status)
	require.Equal(t, "provider", *stored.ProviderMessageID)
	got, _ := h.store.Run(run.ID)
	require.Equal(t, "sent", got.Status)
	require.Zero(t, got.SentCount)
	require.Equal(t, 1, got.FailedCount)
	require.Len(t, h.audit.Records(), 1)
}

type bulkPublisher struct {
	jobs []*managev1.SendBulkEmailBatchEvent
	err  error
}

func (p *bulkPublisher) PublishSendBulkEmail(_ context.Context, j *managev1.SendBulkEmailBatchEvent) error {
	p.jobs = append(p.jobs, j)
	return p.err
}

func TestDispatcherSealsZeroAudienceTerminalAndQueuesDueAudience(t *testing.T) {
	for _, kind := range []string{"campaign empty", "campaign due", "legal empty", "future", "enqueue rollback"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t)
			c, run, _ := deliveryFixture(h)
			run.Status = "scheduled"
			run.ScheduledAt = h.now
			c.Status = campaign.CampaignStatusScheduled
			h.store.PutCampaign(c)
			h.store.PutRun(run, campaigntest.Target())
			publisher := &bulkPublisher{}
			dispatcher := campaign.NewCampaignDeliveryDispatcher(publisher, func() time.Time { return h.now })
			switch kind {
			case "campaign due", "enqueue rollback":
				h.store.Candidates = []campaign.DeliveryRecipient{{RecipientEmail: "r@example.test"}}
			case "legal empty":
				run.RunKind = campaign.EmailDeliveryRunKindLegalNotice
				run.CampaignID = nil
				h.store.PutRun(run, campaigntest.Target())
			case "future":
				run.ScheduledAt = h.now.Add(time.Hour)
				h.store.PutRun(run, campaigntest.Target())
			}
			failure := errors.New("queue unavailable")
			if kind == "enqueue rollback" {
				h.store.Fail("Enqueue", failure)
			}
			published := false
			err := h.transaction(func(ctx context.Context, j campaign.Joined) error {
				var err error
				published, err = dispatcher.DispatchEmailDeliveryRun(ctx, j, run.ID, false)
				return err
			})
			if kind == "enqueue rollback" {
				require.ErrorIs(t, err, failure)
				stored, _ := h.store.Run(run.ID)
				require.Equal(t, "scheduled", stored.Status)
				require.Empty(t, h.store.Queued())
				return
			}
			require.NoError(t, err)
			stored, _ := h.store.Run(run.ID)
			switch kind {
			case "campaign empty":
				require.False(t, published)
				require.Equal(t, "failed", stored.Status)
				require.Len(t, h.audit.Records(), 1)
			case "legal empty":
				require.False(t, published)
				require.Equal(t, "skipped", stored.Status)
				require.Empty(t, h.audit.Records())
			case "campaign due":
				require.True(t, published)
				require.Equal(t, "sending", stored.Status)
				require.Len(t, h.store.Queued(), 1)
			case "future":
				require.False(t, published)
				require.Equal(t, "scheduled", stored.Status)
			}
		})
	}
}
