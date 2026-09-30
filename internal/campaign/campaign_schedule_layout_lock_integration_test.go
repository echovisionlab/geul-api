//go:build integration

package campaign

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	emailauthoringadapter "github.com/echovisionlab/geul-api/internal/adapters/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestScheduleCampaignAndDeleteReferencedLayoutDoNotDeadlockIntegration(t *testing.T) {
	db := newCampaignConcurrentIntegrationDB(t)
	ctx, spiceDB := testutil.IntegrationAdminContext(t, db)
	contentBlocks := testutil.NewEmailContentBlockStore(t, spiceDB)
	references := emailauthoringadapter.NewCampaignDeliveryReferences()
	var campaignID, layoutID string
	t.Cleanup(func() {
		cleanupConcurrentCampaignSnapshotFixture(t, db, ctx, spiceDB, "", campaignID, layoutID)
	})

	layout, err := emailauthoring.NewEmailLayoutService(
		db,
		"https://cdn.example.test",
		"https://www.example.test",
		spiceDB,
		emailauthoring.WithEmailLayoutCampaignDeliveryReferences(references),
		emailauthoring.WithEmailLayoutContentBlockStore(contentBlocks),
	).CreateEmailLayout(ctx, connect.NewRequest(&managev1.CreateEmailLayoutRequest{
		Name:         "Schedule layout race " + uuid.NewString(),
		Key:          "schedule_layout_race_" + strings.ReplaceAll(uuid.NewString(), "-", "_"),
		HtmlContent:  "<html><body>{{content}}</body></html>",
		SourceLocale: "en",
	}))
	require.NoError(t, err)
	layoutID = layout.Msg.Id

	campaignService := func(operationDB *gorm.DB) *CampaignService {
		return NewCampaignService(
			operationDB,
			newCampaignRuntimeFixture(nil, nil),
			"https://cdn.example.test",
			"https://www.example.test",
			spiceDB,
			WithCampaignContentBlockStore(contentBlocks),
			WithCampaignEmailAuthoring(campaignEmailAuthoringConcurrencyFixture{}),
			WithCampaignEmailDelivery(NewCampaignDeliveryRuntime(spiceDB, nil)),
		)
	}
	created, err := campaignService(db).CreateCampaign(ctx, connect.NewRequest(&managev1.CreateCampaignRequest{
		Name:         "Schedule layout race " + uuid.NewString(),
		Subject:      "Schedule layout race",
		SourceLocale: "en",
		Target:       campaignAllTarget(),
	}))
	require.NoError(t, err)
	campaignID = created.Msg.Campaign.Id
	publishCampaignSourceBlocksForIntegration(
		t,
		db,
		spiceDB,
		campaignID,
		"Schedule layout lock-order race",
	)
	_, err = campaignService(db).UpdateCampaignConfiguration(
		ctx,
		connect.NewRequest(&managev1.UpdateCampaignConfigurationRequest{
			Id:             campaignID,
			TargetMode:     managev1.CampaignTargetMode_CAMPAIGN_TARGET_MODE_ALL,
			LayoutId:       &layoutID,
			RecipientScope: managev1.CampaignRecipientScope_CAMPAIGN_RECIPIENT_SCOPE_SUBSCRIBED_USERS,
		}),
	)
	require.NoError(t, err)

	deleteApplication := durableEmailApplicationName("schedule_layout_delete")
	scheduleApplication := durableEmailApplicationName("schedule_layout_send")
	deleteDB := newNamedCampaignDeliveryIntegrationDB(t, deleteApplication)
	scheduleDB := newNamedCampaignDeliveryIntegrationDB(t, scheduleApplication)
	start := make(chan struct{})
	deleteDone := startDurableEmailOperation(t, ctx, func(operationCtx context.Context) error {
		<-start
		_, deleteErr := emailauthoring.NewEmailLayoutService(
			deleteDB,
			"https://cdn.example.test",
			"https://www.example.test",
			spiceDB,
			emailauthoring.WithEmailLayoutCampaignDeliveryReferences(references),
			emailauthoring.WithEmailLayoutContentBlockStore(contentBlocks),
		).DeleteEmailLayout(operationCtx, connect.NewRequest(&managev1.DeleteEmailLayoutRequest{Id: layoutID}))
		return deleteErr
	})
	scheduleDone := startDurableEmailOperation(t, ctx, func(operationCtx context.Context) error {
		<-start
		_, scheduleErr := campaignService(scheduleDB).ScheduleCampaign(
			operationCtx,
			connect.NewRequest(&managev1.ScheduleCampaignRequest{
				Id:             campaignID,
				ScheduledAt:    timestamppb.New(time.Now().UTC().Add(time.Hour)),
				RecipientScope: managev1.CampaignRecipientScope_CAMPAIGN_RECIPIENT_SCOPE_SUBSCRIBED_USERS,
			}),
		)
		return scheduleErr
	})
	close(start)

	deleteErr := waitForDurableEmailOperation(t, deleteDone)
	scheduleErr := waitForDurableEmailOperation(t, scheduleDone)
	require.NoError(t, scheduleErr)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(deleteErr), deleteErr)
	var persisted model.Campaign
	require.NoError(t, db.First(&persisted, "id = ?", campaignID).Error)
	require.Equal(t, managev1.CampaignStatus_CAMPAIGN_STATUS_SCHEDULED.String(), persisted.Status)
	require.Equal(t, layoutID, ptrStringValue(persisted.LayoutID))
}
