//go:build integration

package campaign

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"
	"gorm.io/gorm"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type captureFailingCampaignAuditAppender struct {
	targetID string
}

func (writer *captureFailingCampaignAuditAppender) AppendDomainAuditInTransaction(
	_ context.Context,
	_ *gorm.DB,
	record sharedtelemetry.AuditRecord,
) error {
	writer.targetID = record.TargetID
	return errors.New("audit unavailable")
}

func campaignAllTarget() *managev1.CreateCampaignRequest_All {
	return &managev1.CreateCampaignRequest_All{All: &emptypb.Empty{}}
}

func TestCampaignAuthorizationIsSynchronousIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.123")
	require.NoError(t, err)
	ctx := auth.WithUser(sharedtelemetry.WithRequestContext(context.Background(), requestContext), admin.AuthUserInfo())
	contentBlocks := testutil.NewEmailContentBlockStore(t, stack.SpiceDBClient)
	campaigns := NewCampaignService(
		db,
		newCampaignRuntimeFixture(nil, nil),
		"",
		"",
		stack.SpiceDBClient,
		WithCampaignContentBlockStore(contentBlocks),
		WithCampaignEmailDelivery(NewCampaignDeliveryRuntime(stack.SpiceDBClient, nil)),
	)
	campaign, err := campaigns.CreateCampaign(ctx, connect.NewRequest(&managev1.CreateCampaignRequest{
		Name: "Synchronous campaign authorization", Subject: "subject", SourceLocale: "en", Target: campaignAllTarget(),
	}))
	require.NoError(t, err)
	requireResourcePermission(
		t,
		ctx,
		stack.SpiceDBClient,
		campaign.Msg.Campaign.Id,
		true,
	)
	failingCampaignAudit := &captureFailingCampaignAuditAppender{}
	failingCampaigns := NewAuditedCampaignService(
		db,
		newCampaignRuntimeFixture(nil, apitelemetry.NewDurableWriter(db)),
		"",
		"",
		failingCampaignAudit,
		stack.SpiceDBClient,
		WithCampaignContentBlockStore(contentBlocks),
		WithCampaignEmailDelivery(NewCampaignDeliveryRuntime(stack.SpiceDBClient, nil)),
	)
	_, err = failingCampaigns.DeleteCampaign(ctx, connect.NewRequest(&managev1.DeleteCampaignRequest{Id: campaign.Msg.Campaign.Id}))
	require.Error(t, err)
	require.Equal(t, campaign.Msg.Campaign.Id, failingCampaignAudit.targetID)
	var preservedCampaignRows int64
	require.NoError(t, db.Table("campaign").Where("id = ?", campaign.Msg.Campaign.Id).Count(&preservedCampaignRows).Error)
	require.EqualValues(t, 1, preservedCampaignRows)
	requireResourcePermission(
		t,
		ctx,
		stack.SpiceDBClient,
		campaign.Msg.Campaign.Id,
		true,
	)

	_, err = campaigns.DeleteCampaign(ctx, connect.NewRequest(&managev1.DeleteCampaignRequest{Id: campaign.Msg.Campaign.Id}))
	require.NoError(t, err)
	requireResourcePermission(
		t,
		ctx,
		stack.SpiceDBClient,
		campaign.Msg.Campaign.Id,
		false,
	)
}

func requireResourcePermission(
	t *testing.T,
	ctx context.Context,
	spiceDB *auth.SpiceDBClient,
	resourceID string,
	expected bool,
) {
	t.Helper()
	can, err := policyv1.Campaign.Manage(resourceID)
	require.NoError(t, err)
	decision, err := auth.AuthorizationDecision(ctx, can)
	require.NoError(t, err)
	allowed, err := spiceDB.Can(ctx, decision)
	require.NoError(t, err)
	require.Equal(t, expected, allowed)
}
