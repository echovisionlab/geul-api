//go:build integration

package campaign

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCampaignRecipientPersonalDataAccessPersistsExactScopeIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	target := stack.CreateUser(t, policyv1.Role.User().ID())
	now := time.Now().UTC().Truncate(time.Second)
	campaignID := uuid.NewString()
	campaignDocumentID := uuid.NewString()
	runID := uuid.NewString()
	recipientID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO content_document (id, profile) VALUES (?, 'email')`,
		campaignDocumentID,
	).Error)
	require.NoError(t, db.Create(&model.Campaign{
		ID: campaignID, ContentDocumentID: &campaignDocumentID,
		Name: "Access campaign", Subject: "Access campaign",
		Status: "CAMPAIGN_STATUS_SENT", TargetMode: model.CampaignTargetModeAll,
		RecipientScope: campaignRecipientScopeAllMatchingUsers,
		CreatedAt:      now, UpdatedAt: now,
	}).Error)
	seedCampaignPersonalAccessPolicy(t, stack.SpiceDBClient, campaignID)
	require.NoError(t, db.Create(&model.CampaignDeliveryRun{
		ID: runID, RunKind: EmailDeliveryRunKindCampaign, CampaignID: &campaignID,
		Status: CampaignDeliveryRunStatusSent, ScheduledAt: now, CompletedAt: &now,
		TemplateData: model.JSONFields{}, RenderSnapshot: model.JSONFields{
			"subject": "Access campaign", "content_html": "<p>Access campaign</p>",
			"source_locale": "en", "translations": []any{map[string]any{
				"locale": "en", "subject": "Access campaign", "content_html": "<p>Access campaign</p>",
			}},
		},
		SnapshotSchemaVersion: 1, DefinitionSealed: true, SourceCampaignUpdatedAt: &now,
		TargetQueryVersion:   CampaignDeliveryTargetQueryVersion,
		TargetMode:           CampaignDeliveryTargetModeAllUsers,
		TargetRecipientScope: campaignRecipientScopeAllMatchingUsers,
		TargetCount:          1, SentCount: 1, CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, db.Create(&model.CampaignDeliveryRecipient{
		ID: recipientID, RunID: runID,
		RecipientEmail:           "access-target@example.test",
		NormalizedRecipientEmail: "access-target@example.test",
		IdentityID:               &target.IdentityID, MemberID: &target.MemberID,
		RecipientContextType: BulkEmailContextAccountCurrent,
		Status:               CampaignDeliveryRecipientStatusSent,
		TerminalAt:           &now, CreatedAt: now, UpdatedAt: now,
	}).Error)

	writer := apitelemetry.NewDurableWriter(db)
	service := NewAuditedCampaignService(
		db, newCampaignRuntimeFixture(nil, writer), "", "", writer, stack.SpiceDBClient,
		WithCampaignEmailDelivery(NewCampaignDeliveryRuntime(stack.SpiceDBClient, nil)),
	)
	ctx := campaignPersonalAccessAdminContext(t, admin)
	_, err := service.GetCampaignRecipients(
		ctx,
		connect.NewRequest(&managev1.GetCampaignRecipientsRequest{Id: campaignID}),
	)
	require.NoError(t, err)

	type storedAccess struct {
		Action        string
		ActorMemberID string
		RequestID     string
		SourceIP      string
		SubjectType   string
		SubjectID     string
		AccessKind    string
		DataCategory  string
	}
	var records []storedAccess
	require.NoError(t, db.Raw(`
		SELECT action, actor_member_id::text AS actor_member_id,
		       request_id::text AS request_id, host(source_ip) AS source_ip,
		       attributes->>'subject_type' AS subject_type,
		       attributes->>'subject_id' AS subject_id,
		       attributes->>'access_kind' AS access_kind,
		       attributes->>'data_category' AS data_category
		FROM security_access
		WHERE action = 'personal_data.accessed' AND actor_member_id = ?::uuid
		ORDER BY occurred_at, access_id
	`, admin.MemberID).Scan(&records).Error)
	require.Len(t, records, 1)
	record := records[0]
	require.Equal(t, string(sharedtelemetry.SecurityPersonalDataAccessed), record.Action)
	require.Equal(t, admin.MemberID, record.ActorMemberID)
	require.NotEmpty(t, record.RequestID)
	require.Equal(t, "198.51.100.23", record.SourceIP)
	require.Equal(t, "campaign", record.SubjectType)
	require.Equal(t, campaignID, record.SubjectID)
	require.Equal(t, "read", record.AccessKind)
	require.Equal(t, "campaign_recipients", record.DataCategory)
	require.NotContains(t, strings.Join(
		[]string{record.SubjectType, record.SubjectID, record.DataCategory}, ":",
	), "private-value@example.test")
}

func campaignPersonalAccessAdminContext(
	t *testing.T,
	admin *testutil.OryUser,
) context.Context {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPublicRequestContext("198.51.100.23")
	require.NoError(t, err)
	return auth.WithUser(
		sharedtelemetry.WithRequestContext(t.Context(), requestContext),
		admin.AuthUserInfo(),
	)
}

func seedCampaignPersonalAccessPolicy(
	t *testing.T,
	spiceDB *auth.SpiceDBClient,
	campaignID string,
) {
	t.Helper()
	policy, err := policyv1.Campaign.TouchPolicy(campaignID)
	require.NoError(t, err)
	_, err = spiceDB.ApplyRelationships(t.Context(), policy)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		deletePolicy, deleteErr := policyv1.Campaign.DeletePolicy(campaignID)
		require.NoError(t, deleteErr)
		_, deleteErr = spiceDB.ApplyRelationships(cleanupCtx, deletePolicy)
		require.NoError(t, deleteErr)
	})
}
