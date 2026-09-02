//go:build integration

package campaign

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestCampaignServiceCRUDIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	ctx := auth.WithUser(context.Background(), admin.AuthUserInfo())
	contentBlocks := testutil.NewEmailContentBlockStore(t, stack.SpiceDBClient)
	campaignSvc := NewCampaignService(
		db, newCampaignRuntimeFixture(nil, nil), "https://cdn.example.com", "https://example.com", stack.SpiceDBClient,
		WithCampaignContentBlockStore(contentBlocks),
		WithCampaignEmailDelivery(NewCampaignDeliveryRuntime(stack.SpiceDBClient, nil)),
	)

	req := connect.NewRequest(&managev1.CreateCampaignRequest{
		Name:         "Campaign Integration Name",
		Subject:      "Campaign Integration Subject",
		SourceLocale: "ko",
		Target:       campaignAllTarget(),
	})
	created, err := campaignSvc.CreateCampaign(ctx, req)
	require.NoError(t, err)
	require.NotEmpty(t, created.Msg.Campaign.Id)
	require.Equal(t, "Campaign Integration Name", created.Msg.Campaign.Name)
	require.Equal(t, "Campaign Integration Subject", created.Msg.Campaign.Subject)
	require.Equal(t, "ko", created.Msg.Campaign.SourceLocale)
	require.Equal(t, managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT, created.Msg.Campaign.Status)
	require.Equal(
		t,
		managev1.CampaignTargetMode_CAMPAIGN_TARGET_MODE_ALL,
		created.Msg.Campaign.TargetMode,
	)
	requireCampaignSourceLocaleRow(t, db, created.Msg.Campaign.Id, "ko", "Campaign Integration Subject")

	fetched, err := campaignSvc.GetCampaign(ctx, connect.NewRequest(&managev1.GetCampaignRequest{Id: created.Msg.Campaign.Id}))
	require.NoError(t, err)
	require.Equal(t, created.Msg.Campaign.Id, fetched.Msg.Campaign.Id)
	require.Equal(t, "Campaign Integration Subject", fetched.Msg.Campaign.Subject)

	nextName := "Campaign Integration Name Updated"
	renamed, err := campaignSvc.UpdateCampaignName(ctx, connect.NewRequest(&managev1.UpdateCampaignNameRequest{
		Id:   created.Msg.Campaign.Id,
		Name: nextName,
	}))
	require.NoError(t, err)
	require.Equal(t, nextName, renamed.Msg.Name)
	require.True(t, renamed.Msg.Changed)
	require.Equal(t, "Campaign Integration Subject", fetched.Msg.Campaign.Subject)
	requireCampaignSourceLocaleRow(t, db, created.Msg.Campaign.Id, "ko", "Campaign Integration Subject")

	deleted, err := campaignSvc.DeleteCampaign(ctx, connect.NewRequest(&managev1.DeleteCampaignRequest{Id: created.Msg.Campaign.Id}))
	require.NoError(t, err)
	require.True(t, deleted.Msg.Success)
	var campaignRows int64
	require.NoError(t, db.Table("campaign").Where("id = ?", created.Msg.Campaign.Id).Count(&campaignRows).Error)
	require.Zero(t, campaignRows)
	requireNoCampaignTranslationRows(t, db, created.Msg.Campaign.Id)
}

func requireCampaignSourceLocaleRow(
	t *testing.T,
	db *gorm.DB,
	campaignID string,
	sourceLocale string,
	subject string,
) {
	t.Helper()

	var state struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	stateResult := db.Raw(
		`SELECT source_locale FROM campaign WHERE id = ? LIMIT 1`,
		campaignID,
	).Scan(&state)
	require.NoError(t, stateResult.Error)
	require.Equal(t, int64(1), stateResult.RowsAffected)
	require.Equal(t, sourceLocale, state.SourceLocale)

	var row struct {
		Subject string `gorm:"column:subject"`
	}
	rowResult := db.Raw(
		`SELECT translation.subject
		   FROM campaign_translation AS translation
		  WHERE translation.entity_id = ?
		    AND translation.locale = ?
		  LIMIT 1`,
		campaignID,
		sourceLocale,
	).Scan(&row)
	require.NoError(t, rowResult.Error)
	require.Equal(t, int64(1), rowResult.RowsAffected)
	require.Equal(t, subject, row.Subject)
}

func requireNoCampaignTranslationRows(t *testing.T, db *gorm.DB, campaignID string) {
	t.Helper()

	var count int64
	result := db.Raw(
		`SELECT COUNT(*)
		   FROM campaign_translation
		  WHERE entity_id = ?`,
		campaignID,
	).Scan(&count)
	require.NoError(t, result.Error)
	require.Zero(t, count)
}
