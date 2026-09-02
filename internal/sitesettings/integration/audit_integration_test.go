//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"connectrpc.com/connect"
	sitesettingsadapter "github.com/echovisionlab/geul-api/internal/adapters/sitesettings"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/sitesettings"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
	"gorm.io/gorm"
)

func TestSiteSettingsAuditCommitsExactChangesAndSkipsNoOpIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	identityID := uuid.NewString()
	memberID := seedExternalKratosIdentityWithTraits(t, db, identityID, "Site Settings audit admin")
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	service := sitesettings.NewAuditedSiteSettingService(
		db,
		"https://www.example.test",
		sitesettingsadapter.NewAssets("https://cdn.example.test"),
		sitesettingsadapter.NewReferences(),
		newSiteSettingsOGInvalidatorForTest(db, "https://cdn.example.test"),
		apitelemetry.NewDurableWriter(db),
		spiceDB,
	)
	ctx := siteSettingsAuditedMemberContext(t, identityID, memberID)
	request := connect.NewRequest(&managev1.SetManySettingsRequest{Settings: []*managev1.SiteSetting{
		{Key: "site_title", Value: structpb.NewStringValue("Audit title")},
		{Key: "primary_color", Value: structpb.NewStringValue("#123456")},
		{Key: "site_title", Value: structpb.NewStringValue("Audit title")},
	}})

	_, err := service.SetManySettings(ctx, request)
	require.NoError(t, err)

	var stored struct {
		Action        string
		ActorMemberID string
		RequestID     string
		TargetType    string
		TargetID      string
		ChangedFields pq.StringArray `gorm:"type:text[]"`
	}
	require.NoError(t, db.Raw(`
		SELECT action, actor_member_id::text AS actor_member_id,
		       request_id::text AS request_id, target_type, target_id,
		       ARRAY(SELECT jsonb_array_elements_text(attributes->'changed_fields')) AS changed_fields
		FROM public.domain_audit
	`).Scan(&stored).Error)
	require.Equal(t, string(sharedtelemetry.AuditSiteSettingsUpdated), stored.Action)
	require.Equal(t, memberID, stored.ActorMemberID)
	require.Equal(t, sharedtelemetry.RequestIDFromContext(ctx), stored.RequestID)
	require.Equal(t, "site_settings", stored.TargetType)
	require.Equal(t, "1", stored.TargetID)
	require.Equal(t, pq.StringArray{"primary_color", "site_title"}, stored.ChangedFields)

	_, err = service.SetManySettings(ctx, request)
	require.NoError(t, err)
	var count int64
	require.NoError(t, db.Table("public.domain_audit").Count(&count).Error)
	require.Equal(t, int64(1), count)
}

func TestSiteSettingsAuditFailureRollsBackMutationIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	identityID := uuid.NewString()
	memberID := seedExternalKratosIdentityWithTraits(t, db, identityID, "Site Settings rollback admin")
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	service := sitesettings.NewAuditedSiteSettingService(
		db,
		"https://www.example.test",
		sitesettingsadapter.NewAssets("https://cdn.example.test"),
		sitesettingsadapter.NewReferences(),
		newSiteSettingsOGInvalidatorForTest(db, "https://cdn.example.test"),
		failingDomainAuditAppender{},
		spiceDB,
	)
	_, err := service.SetSetting(
		siteSettingsAuditedMemberContext(t, identityID, memberID),
		connect.NewRequest(&managev1.SetSettingRequest{
			Key: "company_name", Value: structpb.NewStringValue("must roll back"),
		}),
	)
	require.Error(t, err)

	var companyName string
	require.NoError(t, db.Raw(`SELECT company_name FROM public.site_settings WHERE id = 1`).Scan(&companyName).Error)
	require.NotEqual(t, "must roll back", companyName)
}

func TestSiteSettingsNestedOgAuditUsesAggregateFieldAndRollsBackIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	identityID := uuid.NewString()
	memberID := seedExternalKratosIdentityWithTraits(t, db, identityID, "Site Settings OG audit admin")
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	ctx := siteSettingsAuditedMemberContext(t, identityID, memberID)
	service := sitesettings.NewAuditedSiteSettingService(
		db, "https://www.example.test", sitesettingsadapter.NewAssets("https://cdn.example.test"), sitesettingsadapter.NewReferences(),
		newSiteSettingsOGInvalidatorForTest(db, "https://cdn.example.test"), apitelemetry.NewDurableWriter(db), spiceDB,
	)

	_, err := service.SetSetting(ctx, connect.NewRequest(&managev1.SetSettingRequest{
		Key: "og_image_config.home", Value: structpb.NewStructValue(&structpb.Struct{Fields: map[string]*structpb.Value{
			"background_color": structpb.NewStringValue("#123456"),
		}}),
	}))
	require.NoError(t, err)

	var attributes struct {
		ChangedFields pq.StringArray `gorm:"type:text[]"`
	}
	require.NoError(t, db.Raw(`SELECT ARRAY(SELECT jsonb_array_elements_text(attributes->'changed_fields')) AS changed_fields
		FROM domain_audit WHERE action = ? ORDER BY occurred_at DESC, audit_id DESC LIMIT 1`, sharedtelemetry.AuditSiteSettingsUpdated).Scan(&attributes).Error)
	require.Equal(t, pq.StringArray{"og_image_config"}, attributes.ChangedFields)

	failing := sitesettings.NewAuditedSiteSettingService(
		db, "https://www.example.test", sitesettingsadapter.NewAssets("https://cdn.example.test"), sitesettingsadapter.NewReferences(),
		newSiteSettingsOGInvalidatorForTest(db, "https://cdn.example.test"), failingDomainAuditAppender{}, spiceDB,
	)
	_, err = failing.SetSetting(ctx, connect.NewRequest(&managev1.SetSettingRequest{
		Key: "og_image_config.content", Value: structpb.NewStructValue(&structpb.Struct{Fields: map[string]*structpb.Value{
			"background_color": structpb.NewStringValue("#654321"),
		}}),
	}))
	require.Error(t, err)

	var rawConfig string
	require.NoError(t, db.Raw(`SELECT og_image_config FROM site_settings WHERE id = 1`).Scan(&rawConfig).Error)
	var config map[string]map[string]string
	require.NoError(t, json.Unmarshal([]byte(rawConfig), &config))
	require.Equal(t, "#123456", config["home"]["background_color"])
	require.NotContains(t, config["content"], "background_color")
}

type failingDomainAuditAppender struct{}

func (failingDomainAuditAppender) AppendDomainAuditInTransaction(
	context.Context,
	*gorm.DB,
	sharedtelemetry.AuditRecord,
) error {
	return errors.New("audit unavailable")
}

func siteSettingsAuditedMemberContext(t *testing.T, identityID, memberID string) context.Context {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.77")
	require.NoError(t, err)
	ctx := sharedtelemetry.WithRequestContext(t.Context(), requestContext)
	return auth.WithUser(ctx, &auth.UserInfo{
		SessionID: auth.SessionID(uuid.NewString()), IdentityID: auth.IdentityID(identityID),
		MemberID: auth.MemberID(memberID), Authenticated: true, Onboarded: true,
	})
}
