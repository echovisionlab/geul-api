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
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestScheduleCampaignAndDeleteReferencedLayoutDoNotDeadlockIntegration(t *testing.T) {
	t.Parallel()
	stack := newCampaignLocalKratosStack(t)
	db := openCampaignIntegrationDB(t, stack)
	ctx, spiceDB := testutil.IntegrationAdminAuditContext(t, db)
	contentBlocks := testutil.NewEmailContentBlockStore(t, spiceDB)
	references := emailauthoringadapter.NewCampaignDeliveryReferences()
	var campaignID, layoutID string
	t.Cleanup(func() {
		cleanupConcurrentCampaignSnapshotFixture(t, db, ctx, spiceDB, "", campaignID, layoutID)
	})

	layout, err := emailauthoring.NewLayoutConnectHandler(newEmailAuthoringService(t, emailAuthoringDeps{DB: db, CDNDomain: "https://cdn.example.test", SiteOrigin: "https://www.example.test", SpiceDB: spiceDB, References: references, ContentBlocks: contentBlocks})).CreateEmailLayout(ctx, connect.NewRequest(&managev1.CreateEmailLayoutRequest{
		Name:         "Schedule layout race " + uuid.NewString(),
		Key:          "schedule_layout_race_" + strings.ReplaceAll(uuid.NewString(), "-", "_"),
		HtmlContent:  "<html><body>{{content}}</body></html>",
		SourceLocale: "en",
	}))
	require.NoError(t, err)
	layoutID = layout.Msg.Id

	campaignService := func(operationDB *gorm.DB) *ConnectHandler {
		return NewConnectHandler(newTestService(t, Deps{DB: operationDB, Runtime: newCampaignRuntimeFixture(nil, nil), CDNDomain: "https://cdn.example.test", SiteOrigin: "https://www.example.test", SpiceDB: spiceDB, ContentBlocks: contentBlocks, EmailAuthoring: campaignEmailAuthoringConcurrencyFixture{}, EmailDelivery: NewCampaignDeliveryRuntime(spiceDB, nil)}))
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
	targetMode := managev1.CampaignTargetMode_CAMPAIGN_TARGET_MODE_ALL
	recipientScope := managev1.CampaignRecipientScope_CAMPAIGN_RECIPIENT_SCOPE_SUBSCRIBED_USERS
	_, err = campaignService(db).UpdateCampaignConfiguration(
		ctx,
		connect.NewRequest(&managev1.UpdateCampaignConfigurationRequest{
			Id: campaignID, TargetMode: &targetMode, LayoutId: &layoutID, RecipientScope: &recipientScope,
		}),
	)
	require.NoError(t, err)

	deleteApplication := durableEmailApplicationName("schedule_layout_delete")
	scheduleApplication := durableEmailApplicationName("schedule_layout_send")
	deleteDB := newNamedCampaignDeliveryIntegrationDBForStack(t, stack, deleteApplication)
	scheduleDB := newNamedCampaignDeliveryIntegrationDBForStack(t, stack, scheduleApplication)
	start := make(chan struct{})
	deleteDone := startDurableEmailOperation(t, ctx, func(operationCtx context.Context) error {
		<-start
		_, deleteErr := emailauthoring.NewLayoutConnectHandler(newEmailAuthoringService(t, emailAuthoringDeps{DB: deleteDB, CDNDomain: "https://cdn.example.test", SiteOrigin: "https://www.example.test", SpiceDB: spiceDB, References: references, ContentBlocks: contentBlocks})).DeleteEmailLayout(operationCtx, connect.NewRequest(&managev1.DeleteEmailLayoutRequest{Id: layoutID}))
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

	deleteErr := <-deleteDone
	scheduleErr := <-scheduleDone
	require.NoError(t, scheduleErr)
	require.Equal(t, connect.CodeFailedPrecondition, errs.CodeOf(deleteErr), deleteErr)
	var persisted model.Campaign
	require.NoError(t, db.First(&persisted, "id = ?", campaignID).Error)
	require.Equal(t, managev1.CampaignStatus_CAMPAIGN_STATUS_SCHEDULED.String(), persisted.Status)
	require.Equal(t, layoutID, ptrStringValue(persisted.LayoutID))
}
