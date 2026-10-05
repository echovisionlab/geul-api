//go:build integration

package campaign

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
)

func TestCampaignDomainAuditMemberMutationsAndRollbackIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	identityID, memberID := admin.IdentityID, admin.MemberID
	spiceDB := stack.SpiceDBClient
	ctx := testutil.NewAuditContext(t, identityID, memberID)
	service := NewAuditedCampaignService(
		db, newCampaignRuntimeFixture(nil, apitelemetry.NewDurableWriter(db)),
		"https://cdn.example.test", "https://www.example.test",
		apitelemetry.NewDurableWriter(db), spiceDB,
		WithCampaignContentBlockStore(testutil.NewEmailContentBlockStore(t, spiceDB)),
	)

	created, err := service.CreateCampaign(ctx, connect.NewRequest(&managev1.CreateCampaignRequest{Name: "Audit campaign", Subject: "Audit subject", SourceLocale: "en", Target: campaignAllTarget()}))
	require.NoError(t, err)
	id := created.Msg.Campaign.Id
	_, err = service.UpdateCampaignName(ctx, connect.NewRequest(&managev1.UpdateCampaignNameRequest{Id: id, Name: "Audit campaign renamed"}))
	require.NoError(t, err)
	targetMode := managev1.CampaignTargetMode_CAMPAIGN_TARGET_MODE_ALL
	recipientScope := managev1.CampaignRecipientScope_CAMPAIGN_RECIPIENT_SCOPE_ALL_MATCHING_USERS
	subscribedScope := managev1.CampaignRecipientScope_CAMPAIGN_RECIPIENT_SCOPE_SUBSCRIBED_USERS
	// Presence of the optional fields defines a patch; repeating it is a no-op.
	configured, err := service.UpdateCampaignConfiguration(ctx, connect.NewRequest(&managev1.UpdateCampaignConfigurationRequest{
		Id: id, TargetMode: &targetMode, RecipientScope: &recipientScope,
	}))
	require.NoError(t, err)
	require.True(t, configured.Msg.Changed)
	noOp, err := service.UpdateCampaignConfiguration(ctx, connect.NewRequest(&managev1.UpdateCampaignConfigurationRequest{
		Id: id, TargetMode: &targetMode, RecipientScope: &recipientScope,
	}))
	require.NoError(t, err)
	require.False(t, noOp.Msg.Changed)
	// A recipient-only patch must keep the target mode and segment pair.
	recipientOnly, err := service.UpdateCampaignConfiguration(ctx, connect.NewRequest(&managev1.UpdateCampaignConfigurationRequest{
		Id: id, RecipientScope: &subscribedScope,
	}))
	require.NoError(t, err)
	require.True(t, recipientOnly.Msg.Changed)
	require.Equal(t, targetMode, recipientOnly.Msg.TargetMode)
	require.Nil(t, recipientOnly.Msg.SegmentId)
	require.Equal(t, subscribedScope, recipientOnly.Msg.RecipientScope)
	// A target pair patch must not reset the omitted recipient scope.
	targetOnly, err := service.UpdateCampaignConfiguration(ctx, connect.NewRequest(&managev1.UpdateCampaignConfigurationRequest{
		Id: id, TargetMode: &targetMode,
	}))
	require.NoError(t, err)
	require.False(t, targetOnly.Msg.Changed)
	require.Equal(t, subscribedScope, targetOnly.Msg.RecipientScope)
	require.Nil(t, targetOnly.Msg.SegmentId)
	// A layout-only patch must preserve target and recipient fields.
	emptyLayoutID := ""
	layoutOnly, err := service.UpdateCampaignConfiguration(ctx, connect.NewRequest(&managev1.UpdateCampaignConfigurationRequest{
		Id: id, LayoutId: &emptyLayoutID,
	}))
	require.NoError(t, err)
	require.False(t, layoutOnly.Msg.Changed)
	require.Equal(t, targetMode, layoutOnly.Msg.TargetMode)
	require.Equal(t, subscribedScope, layoutOnly.Msg.RecipientScope)
	// Empty requests are rejected instead of replacing omitted configuration.
	_, err = service.UpdateCampaignConfiguration(ctx, connect.NewRequest(&managev1.UpdateCampaignConfigurationRequest{Id: id}))
	require.Error(t, err)
	segmentID := "segment-1"
	_, err = service.UpdateCampaignConfiguration(ctx, connect.NewRequest(&managev1.UpdateCampaignConfigurationRequest{
		Id: id, SegmentId: &segmentID,
	}))
	require.Error(t, err)
	segmentMode := managev1.CampaignTargetMode_CAMPAIGN_TARGET_MODE_SEGMENT
	_, err = service.UpdateCampaignConfiguration(ctx, connect.NewRequest(&managev1.UpdateCampaignConfigurationRequest{
		Id: id, TargetMode: &segmentMode,
	}))
	require.Error(t, err)
	// A single-field patch restores recipient scope without replacing other fields.
	restoredScope, err := service.UpdateCampaignConfiguration(ctx, connect.NewRequest(&managev1.UpdateCampaignConfigurationRequest{
		Id: id, RecipientScope: &recipientScope,
	}))
	require.NoError(t, err)
	require.True(t, restoredScope.Msg.Changed)
	require.Equal(t, targetMode, restoredScope.Msg.TargetMode)
	require.Equal(t, recipientScope, restoredScope.Msg.RecipientScope)

	var rows []campaignAuditRow
	require.NoError(t, db.Raw(`SELECT action, target_type, target_id, actor_member_id::text AS actor_member_id, request_id::text AS request_id, attributes FROM domain_audit WHERE target_type = 'campaign' AND target_id = ? ORDER BY occurred_at, audit_id`, id).Scan(&rows).Error)
	require.Len(t, rows, 5)
	require.Equal(t, []string{"campaign.created", "campaign.updated", "campaign.updated", "campaign.updated", "campaign.updated"}, []string{rows[0].Action, rows[1].Action, rows[2].Action, rows[3].Action, rows[4].Action})
	for _, row := range rows {
		require.Equal(t, memberID, row.ActorMemberID)
		require.Equal(t, sharedtelemetry.RequestIDFromContext(ctx), row.RequestID)
	}
	require.Contains(t, string(rows[1].Attributes), `"name"`)
	require.Contains(t, string(rows[2].Attributes), `"recipient_scope"`)
	require.Contains(t, string(rows[3].Attributes), `"recipient_scope"`)
	require.Contains(t, string(rows[4].Attributes), `"recipient_scope"`)

	failing := NewAuditedCampaignService(
		db, newCampaignRuntimeFixture(nil, apitelemetry.NewDurableWriter(db)),
		"", "", campaignFailingAuditAppender{}, spiceDB,
		WithCampaignContentBlockStore(testutil.NewEmailContentBlockStore(t, spiceDB)),
	)
	_, err = failing.CreateCampaign(ctx, connect.NewRequest(&managev1.CreateCampaignRequest{Name: "must roll back", Subject: "subject", SourceLocale: "en", Target: campaignAllTarget()}))
	require.Error(t, err)
	var count int64
	require.NoError(t, db.Model(&model.Campaign{}).Where("name = ?", "must roll back").Count(&count).Error)
	require.Zero(t, count)
}

func TestCampaignDeliveryTerminalAuditIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	ensureBulkEmailAudienceKratosIdentityColumns(t, db)
	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	ctx := auth.WithUser(context.Background(), admin.AuthUserInfo())
	created, err := NewCampaignService(
		db, newCampaignRuntimeFixture(nil, nil), "", "", stack.SpiceDBClient,
		WithCampaignContentBlockStore(testutil.NewEmailContentBlockStore(t, stack.SpiceDBClient)),
	).CreateCampaign(ctx, connect.NewRequest(&managev1.CreateCampaignRequest{
		Name: "Terminal audit campaign", Subject: "Terminal audit subject", SourceLocale: "en",
		Target: campaignAllTarget(),
	}))
	require.NoError(t, err)
	campaign := created.Msg.Campaign
	now := time.Now().UTC()
	require.NoError(t, db.Model(&model.Campaign{}).Where("id = ?", campaign.Id).Updates(map[string]any{
		"status": managev1.CampaignStatus_CAMPAIGN_STATUS_SENDING.String(),
	}).Error)
	var storedCampaign model.Campaign
	require.NoError(t, db.First(&storedCampaign, "id = ?", campaign.Id).Error)
	var run *model.CampaignDeliveryRun
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		ref, createErr := createCampaignDeliveryRun(
			t.Context(),
			tx,
			storedCampaign,
			now,
			0,
			nil,
			testutil.NewEmailContentBlockStore(t, stack.SpiceDBClient),
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
		return nil
	}))
	require.NoError(t, db.Model(&model.CampaignDeliveryRun{}).Where("id = ?", run.ID).Updates(map[string]any{
		"status": CampaignDeliveryRunStatusSending, "target_count": 1, "started_at": now,
	}).Error)
	identityID := seedBulkEmailAudienceIdentity(t, db, "terminal-audit-"+testutil.IntegrationUUID()+"@example.test", now)
	memberID := memberIDForCampaignLockTest(t, db, identityID)
	recipient := model.CampaignDeliveryRecipient{
		RunID: run.ID, RecipientEmail: identityEmailForCampaignLockTest(t, db, identityID),
		IdentityID: &identityID, MemberID: &memberID, RecipientContextType: BulkEmailContextNewsletterSubscription,
		Status: CampaignDeliveryRecipientStatusPending, CreatedAt: now, UpdatedAt: now,
	}
	recipient.NormalizedRecipientEmail = emailutil.NormalizeAddressForDelivery(recipient.RecipientEmail)
	require.NoError(t, db.Create(&recipient).Error)
	ctx = campaignRequestContext(t)
	require.NoError(t, MarkCampaignDeliveryRecipientResultWithAudit(
		ctx, db, apitelemetry.NewDurableWriter(db), recipient.ID, CampaignDeliveryRecipientStatusSent, "provider-terminal-audit", "",
		nil,
	))
	var rows []struct {
		ActorService string `gorm:"column:actor_service"`
		RequestID    string `gorm:"column:request_id"`
		Attributes   []byte `gorm:"column:attributes"`
	}
	require.NoError(t, db.Raw(`SELECT actor_service, request_id::text AS request_id, attributes
		FROM domain_audit WHERE target_type = 'campaign' AND target_id = ?`, campaign.Id).Scan(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, string(sharedtelemetry.ServiceBackend), rows[0].ActorService)
	require.Equal(t, sharedtelemetry.RequestIDFromContext(ctx), rows[0].RequestID)
	var attributes struct {
		PreviousState string `json:"previous_state"`
		NewState      string `json:"new_state"`
	}
	require.NoError(t, json.Unmarshal(rows[0].Attributes, &attributes))
	require.Equal(t, "sending", attributes.PreviousState)
	require.Equal(t, "sent", attributes.NewState)

	var persistedCampaign model.Campaign
	require.NoError(t, db.First(&persistedCampaign, "id = ?", campaign.Id).Error)
	require.Equal(t, managev1.CampaignStatus_CAMPAIGN_STATUS_SENT.String(), persistedCampaign.Status)
}

func TestCampaignLifecycleAndDeletionAuditIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	ensureBulkEmailAudienceKratosIdentityColumns(t, db)
	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	identityID, memberID := admin.IdentityID, admin.MemberID
	spiceDB := stack.SpiceDBClient
	ctx := testutil.NewAuditContext(t, identityID, memberID)
	publisher := &recordingCampaignPublisher{}
	service := NewAuditedCampaignService(
		db, newCampaignRuntimeFixture(publisher, apitelemetry.NewDurableWriter(db)),
		"", "", apitelemetry.NewDurableWriter(db), spiceDB,
		WithCampaignContentBlockStore(testutil.NewEmailContentBlockStore(t, spiceDB)),
		WithCampaignEmailDelivery(NewCampaignDeliveryRuntime(spiceDB, publisher)),
	)
	created, err := service.CreateCampaign(ctx, connect.NewRequest(&managev1.CreateCampaignRequest{Name: "Lifecycle campaign", Subject: "Lifecycle subject", SourceLocale: "en", Target: campaignAllTarget()}))
	require.NoError(t, err)
	campaignID := created.Msg.Campaign.Id
	publishCampaignSourceBlocksForIntegration(t, db, spiceDB, campaignID, "Lifecycle body")
	scheduledAt := time.Now().Add(time.Hour).UTC()
	_, err = service.ScheduleCampaign(ctx, connect.NewRequest(&managev1.ScheduleCampaignRequest{
		Id: campaignID, ScheduledAt: timestamppb.New(scheduledAt),
		RecipientScope: managev1.CampaignRecipientScope_CAMPAIGN_RECIPIENT_SCOPE_SUBSCRIBED_USERS,
	}))
	require.NoError(t, err)
	_, err = service.CancelCampaign(ctx, connect.NewRequest(&managev1.CancelCampaignRequest{Id: campaignID}))
	require.NoError(t, err)

	// Send-now is a separate Member-owned delivery-run transition. A sealed run
	// needs an actual eligible recipient, but its per-recipient outcome is not
	// audited until the backend terminal transition (covered above).
	recipientIdentityID := seedBulkEmailAudienceIdentity(t, db, "lifecycle-recipient-"+testutil.IntegrationUUID()+"@example.test", time.Now().UTC())
	_ = recipientIdentityID
	_, err = service.SendCampaignNow(ctx, connect.NewRequest(&managev1.SendCampaignNowRequest{
		Id: campaignID, RecipientScope: managev1.CampaignRecipientScope_CAMPAIGN_RECIPIENT_SCOPE_SUBSCRIBED_USERS,
	}))
	require.NoError(t, err)
	require.Len(t, publisher.sendBulkJobs, 1)

	var lifecycleRows []struct {
		Attributes []byte `gorm:"column:attributes"`
	}
	require.NoError(t, db.Raw(`SELECT attributes FROM domain_audit
		WHERE action = 'campaign.updated' AND target_id = ? ORDER BY occurred_at, audit_id`, campaignID).Scan(&lifecycleRows).Error)
	var changedFields [][]string
	for _, row := range lifecycleRows {
		var attributes struct {
			ChangedFields []string `json:"changed_fields"`
		}
		require.NoError(t, json.Unmarshal(row.Attributes, &attributes))
		changedFields = append(changedFields, attributes.ChangedFields)
	}
	require.Contains(t, changedFields, []string{"schedule"})
	require.Contains(t, changedFields, []string{"status"})
	require.Contains(t, changedFields, []string{"delivery_run"})

	deletable, err := service.CreateCampaign(ctx, connect.NewRequest(&managev1.CreateCampaignRequest{Name: "Deletable campaign", Subject: "Deletable subject", SourceLocale: "en", Target: campaignAllTarget()}))
	require.NoError(t, err)
	_, err = service.DeleteCampaign(ctx, connect.NewRequest(&managev1.DeleteCampaignRequest{Id: deletable.Msg.Campaign.Id}))
	require.NoError(t, err)
	var deletedCount int64
	require.NoError(t, db.Table("domain_audit").Where("action = ? AND target_type = 'campaign' AND target_id = ?", sharedtelemetry.AuditCampaignDeleted, deletable.Msg.Campaign.Id).Count(&deletedCount).Error)
	require.Equal(t, int64(1), deletedCount)
}

type campaignFailingAuditAppender struct{}

type campaignAuditRow struct {
	Action        string
	TargetType    string `gorm:"column:target_type"`
	TargetID      string `gorm:"column:target_id"`
	ActorMemberID string `gorm:"column:actor_member_id"`
	RequestID     string `gorm:"column:request_id"`
	Attributes    []byte `gorm:"column:attributes"`
}

func (campaignFailingAuditAppender) AppendDomainAuditInTransaction(context.Context, *gorm.DB, sharedtelemetry.AuditRecord) error {
	return errors.New("campaign audit unavailable")
}
