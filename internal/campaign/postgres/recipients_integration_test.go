//go:build integration

package campaignpostgres

import (
	"context"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/testutil/contenttest"

	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/campaign/campaigntest"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"

	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	// The atomic selection crosses Member, newsletter and tag rows and uses the
	// bilateral identity join. It needs PostgreSQL's joined reads and constraints.
	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
)

func TestRecipientSelectionAndMaterializationUseEligibleLinkedMembersIntegration(t *testing.T) {
	t.Parallel()
	db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true, BootstrapKratosStub: true}).DB
	identityID := uuid.NewString()
	email := "eligible@example.test"
	orytest.SeedKratosIdentityFixture(t, db, orytest.KratosIdentityFixture{ID: identityID, Email: email})
	memberID := seedCampaignActiveMemberEmailPair(t, db, identityID, email)
	all := &bulkEmailRecipientSelection{Mode: campaign.CampaignDeliveryTargetModeAllUsers}
	count, err := countBulkEmailRecipients(t.Context(), db, nil, all)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	all.RequireNewsletterSubscription = true
	count, err = countBulkEmailRecipients(t.Context(), db, nil, all)
	require.NoError(t, err)
	require.Zero(t, count)
	require.NoError(t, db.Create(&model.NewsletterSubscription{IdentityID: identityID}).Error)
	count, err = countBulkEmailRecipients(t.Context(), db, nil, all)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	tag := model.UserTag{Name: "Campaign recipient tag"}
	require.NoError(t, db.Create(&tag).Error)
	require.NoError(t, db.Create(&model.UserTagMapping{MemberID: memberID, TagID: tag.ID}).Error)
	tagged := &bulkEmailRecipientSelection{Mode: campaign.CampaignDeliveryTargetModeUserTags, MemberTagIDs: []string{tag.ID}}
	count, err = countBulkEmailRecipients(t.Context(), db, nil, tagged)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	require.NoError(t, db.Model(&model.Member{}).Where("id = ?", memberID).Update("onboarded", false).Error)
	count, err = countBulkEmailRecipients(t.Context(), db, nil, tagged)
	require.NoError(t, err)
	require.Zero(t, count)
	require.ErrorContains(t, db.Transaction(func(tx *gorm.DB) error {
		_, err := resolveCampaignDeliveryExcludedMemberPairs(t.Context(), tx, uuid.NewString(), []string{memberID})
		return err
	}), "must be onboarded")
	require.NoError(t, db.Model(&model.Member{}).Where("id = ?", memberID).Update("onboarded", true).Error)
	ports := Ports{ContentBlocks: contenttest.NewEmailContentBlockStore(t)}
	units := mutation.Postgres(db, func(tx *gorm.DB) campaign.Store { return NewStore(tx, ports) })
	h := campaigntest.Harness{Harness: mutationtest.PostgresHarness(db, units)}
	c := campaigntest.SeedCampaign(t, h)
	run := campaigntest.SeedRun(t, h, c, "sending")
	for range 2 {
		require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s campaign.Store) error {
			return campaign.MaterializeCampaignDeliveryRun(ctx, campaign.Joined{Store: s}, run.ID)
		}))
	}
	readErr := mutationtest.Read(t, h.Harness, func(ctx context.Context, s campaign.Store) error {
		n, err := s.RecipientCount(ctx, run.ID)
		require.NoError(t, err)
		require.EqualValues(t, 1, n)
		rows, err := s.PendingRecipients(ctx, run.ID, "", 10, time.Now().UTC())
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, memberID, *rows[0].MemberID)
		require.Equal(t, identityID, *rows[0].IdentityID)
		require.Equal(t, email, rows[0].RecipientEmail)
		return nil
	})
	require.NoError(t, readErr)
}
