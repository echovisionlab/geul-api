//go:build integration

package campaign

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Every public Campaign mutation performs an inexpensive early
// rejection, but its authoritative decision is made after its root lock. Each
// case below revokes the direct global role while that root (or, for create,
// the identity fence) is held. The request must fail without changing product
// state or producing a durable command.
func TestCampaignMutationsRecheckAdminAfterRootLockIntegration(t *testing.T) {
	t.Run("campaign create", func(t *testing.T) {
		db, spiceDB, actorID, actorMemberID := seedCampaignAudienceAuthorityActor(t)
		service := newCampaignAuthorityRaceService(t, db, spiceDB)

		identityLock := db.Begin()
		require.NoError(t, identityLock.Error)
		t.Cleanup(func() { _ = identityLock.Rollback().Error })
		require.NoError(t, identityLock.Exec("SELECT id FROM kratos.identities WHERE id = ?::uuid FOR UPDATE", actorID).Error)
		result := make(chan error, 1)
		go func() {
			_, err := service.CreateCampaign(campaignAudienceAuthorityContext(actorID, actorMemberID), connect.NewRequest(&managev1.CreateCampaignRequest{
				Name: "revoked create", Subject: "revoked create", SourceLocale: "en", Target: &managev1.CreateCampaignRequest_All{All: &emptypb.Empty{}},
			}))
			result <- err
		}()
		requireCampaignAudienceMutationWaiting(t, result)
		testutil.GrantIntegrationGlobalRole(t, spiceDB, actorID, policyv1.Role.User())
		require.NoError(t, identityLock.Commit().Error)
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(<-result))
		var count int64
		require.NoError(t, db.Table("campaign").Where("name = ?", "revoked create").Count(&count).Error)
		require.Zero(t, count)
	})

	for _, test := range []struct {
		name   string
		status string
		invoke func(context.Context, *CampaignService, string) error
		assert func(*testing.T, *gorm.DB, string)
	}{
		{
			name: "campaign name update", status: managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String(),
			invoke: func(ctx context.Context, service *CampaignService, campaignID string) error {
				_, err := service.UpdateCampaignName(ctx, connect.NewRequest(&managev1.UpdateCampaignNameRequest{Id: campaignID, Name: "must not update"}))
				return err
			},
			assert: func(t *testing.T, db *gorm.DB, campaignID string) {
				var name string
				require.NoError(t, db.Table("campaign").Select("name").Where("id = ?", campaignID).Scan(&name).Error)
				require.Equal(t, "authority race", name)
			},
		},
		{
			name: "campaign configuration update", status: managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String(),
			invoke: func(ctx context.Context, service *CampaignService, campaignID string) error {
				_, err := service.UpdateCampaignConfiguration(ctx, connect.NewRequest(&managev1.UpdateCampaignConfigurationRequest{
					Id: campaignID, TargetMode: managev1.CampaignTargetMode_CAMPAIGN_TARGET_MODE_ALL,
					RecipientScope: managev1.CampaignRecipientScope_CAMPAIGN_RECIPIENT_SCOPE_ALL_MATCHING_USERS,
				}))
				return err
			},
			assert: func(t *testing.T, db *gorm.DB, campaignID string) {
				var scope string
				require.NoError(t, db.Table("campaign").Select("recipient_scope").Where("id = ?", campaignID).Scan(&scope).Error)
				require.Equal(t, campaignRecipientScopeSubscribedUsers, scope)
			},
		},
		{
			name: "campaign delete", status: managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String(),
			invoke: func(ctx context.Context, service *CampaignService, campaignID string) error {
				_, err := service.DeleteCampaign(ctx, connect.NewRequest(&managev1.DeleteCampaignRequest{Id: campaignID}))
				return err
			},
			assert: func(t *testing.T, db *gorm.DB, campaignID string) {
				var count int64
				require.NoError(t, db.Table("campaign").Where("id = ?", campaignID).Count(&count).Error)
				require.Equal(t, int64(1), count)
			},
		},
		{
			name: "campaign schedule", status: managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String(),
			invoke: func(ctx context.Context, service *CampaignService, campaignID string) error {
				_, err := service.ScheduleCampaign(ctx, connect.NewRequest(&managev1.ScheduleCampaignRequest{
					Id: campaignID, ScheduledAt: timestamppb.New(time.Now().Add(time.Hour)),
					RecipientScope: managev1.CampaignRecipientScope_CAMPAIGN_RECIPIENT_SCOPE_SUBSCRIBED_USERS,
				}))
				return err
			},
			assert: func(t *testing.T, db *gorm.DB, campaignID string) {
				var status string
				require.NoError(t, db.Table("campaign").Select("status").Where("id = ?", campaignID).Scan(&status).Error)
				require.Equal(t, managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String(), status)
			},
		},
		{
			name: "campaign cancel", status: managev1.CampaignStatus_CAMPAIGN_STATUS_SCHEDULED.String(),
			invoke: func(ctx context.Context, service *CampaignService, campaignID string) error {
				_, err := service.CancelCampaign(ctx, connect.NewRequest(&managev1.CancelCampaignRequest{Id: campaignID}))
				return err
			},
			assert: func(t *testing.T, db *gorm.DB, campaignID string) {
				var status string
				require.NoError(t, db.Table("campaign").Select("status").Where("id = ?", campaignID).Scan(&status).Error)
				require.Equal(t, managev1.CampaignStatus_CAMPAIGN_STATUS_SCHEDULED.String(), status)
			},
		},
		{
			name: "campaign immediate send", status: managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String(),
			invoke: func(ctx context.Context, service *CampaignService, campaignID string) error {
				_, err := service.SendCampaignNow(ctx, connect.NewRequest(&managev1.SendCampaignNowRequest{
					Id: campaignID, RecipientScope: managev1.CampaignRecipientScope_CAMPAIGN_RECIPIENT_SCOPE_SUBSCRIBED_USERS,
				}))
				return err
			},
			assert: func(t *testing.T, db *gorm.DB, campaignID string) {
				var status string
				require.NoError(t, db.Table("campaign").Select("status").Where("id = ?", campaignID).Scan(&status).Error)
				require.Equal(t, managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String(), status)
			},
		},
		{
			name: "campaign test send", status: managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String(),
			invoke: func(ctx context.Context, service *CampaignService, campaignID string) error {
				_, err := service.SendTestCampaign(ctx, connect.NewRequest(&managev1.SendTestCampaignRequest{Id: campaignID, Email: "test@example.test"}))
				return err
			},
			assert: func(*testing.T, *gorm.DB, string) {},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, spiceDB, actorID, actorMemberID := seedCampaignAudienceAuthorityActor(t)
			service := newCampaignAuthorityRaceService(t, db, spiceDB)
			campaignID := seedCampaignAuthorityRaceCampaign(t, db, spiceDB, service, actorID, actorMemberID, test.status)

			rootLock := db.Begin()
			require.NoError(t, rootLock.Error)
			t.Cleanup(func() { _ = rootLock.Rollback().Error })
			require.NoError(t, rootLock.Exec("SELECT id FROM campaign WHERE id = ?::uuid FOR UPDATE", campaignID).Error)
			result := make(chan error, 1)
			go func() {
				result <- test.invoke(campaignAudienceAuthorityContext(actorID, actorMemberID), service, campaignID)
			}()
			requireCampaignAudienceMutationWaiting(t, result)
			testutil.GrantIntegrationGlobalRole(t, spiceDB, actorID, policyv1.Role.User())
			require.NoError(t, rootLock.Commit().Error)
			mutationErr := <-result
			require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(mutationErr), "error: %v", mutationErr)
			test.assert(t, db, campaignID)
		})
	}

}

func newCampaignAuthorityRaceService(t *testing.T, db *gorm.DB, spiceDB *auth.SpiceDBClient) *CampaignService {
	t.Helper()
	return NewCampaignService(
		db,
		newCampaignRuntimeFixture(nil, nil),
		"",
		"",
		spiceDB,
		WithCampaignContentBlockStore(testutil.NewEmailContentBlockStore(t, spiceDB)),
		WithCampaignEmailRendering(campaignAuthorityRaceEmailRendering{}),
	)
}

// campaignAuthorityRaceEmailRendering keeps this authorization-concurrency
// fixture on the same test-recipient projection used by the production
// Campaign adapter. The embedded port deliberately leaves unrelated rendering
// operations unavailable: this test only exercises SendTestCampaign.
type campaignAuthorityRaceEmailRendering struct {
	CampaignEmailRenderingPort
}

func (campaignAuthorityRaceEmailRendering) TestRecipientContext(actorMemberID string) *managev1.SendEmailEvent_TestEmail {
	return email.TestEmailContext(actorMemberID)
}

func seedCampaignAudienceAuthorityActor(t *testing.T) (*gorm.DB, *auth.SpiceDBClient, string, string) {
	t.Helper()
	db := newCampaignConcurrentIntegrationDB(t)
	stack := testutil.SetupOryStack(t)
	actor := stack.CreateUser(t, policyv1.Role.Admin().ID())
	return db, stack.SpiceDBClient, actor.IdentityID, actor.MemberID
}

func seedCampaignAuthorityRaceCampaign(
	t *testing.T,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	service *CampaignService,
	actorID string,
	actorMemberID string,
	status string,
) string {
	t.Helper()
	created, err := service.CreateCampaign(
		campaignAudienceAuthorityContext(actorID, actorMemberID),
		connect.NewRequest(&managev1.CreateCampaignRequest{
			Name:         "authority race",
			Subject:      "authority race",
			SourceLocale: "en",
			Target:       &managev1.CreateCampaignRequest_All{All: &emptypb.Empty{}},
		}),
	)
	require.NoError(t, err)
	campaignID := created.Msg.Campaign.Id
	publishCampaignSourceBlocksForIntegration(t, db, spiceDB, campaignID, "body")
	require.NoError(t, requireCurrentCampaignRenderableContent(t.Context(), db, campaignID))

	now := time.Now().UTC()
	require.NoError(t, db.Model(&model.Campaign{}).Where("id = ?", campaignID).Updates(map[string]any{
		"status":       status,
		"scheduled_at": scheduledAtForAuthorityRace(status, now),
	}).Error)
	t.Cleanup(func() { _ = db.Exec("DELETE FROM campaign WHERE id = ?::uuid", campaignID).Error })
	return campaignID
}

func scheduledAtForAuthorityRace(status string, now time.Time) any {
	if status == managev1.CampaignStatus_CAMPAIGN_STATUS_SCHEDULED.String() {
		return now.Add(time.Hour)
	}
	return nil
}

func campaignAudienceAuthorityContext(identityID, memberID string) context.Context {
	return auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(memberID),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	})
}

func requireCampaignAudienceMutationWaiting(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		require.FailNow(t, "mutation returned before its root lock was released", "error: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
}
