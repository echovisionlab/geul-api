//go:build integration

package maptheme

import (
	"testing"

	"connectrpc.com/connect"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestMapThemeAdminSnapshotUpdateRevisionNoopAndAuditIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	spiceDB := integrationSpiceDB(t)
	admin := mapThemeAdminUser(t)
	ctx := withAuditedRequestContext(t, mapThemeMemberContext(admin))
	input := validCreateMapThemeRequest("Snapshot source")
	input.Settings.CalloutScale = 1.1 // Not exactly representable in persisted float32.
	created, err := mapThemeServiceForTest(t, db, spiceDB).CreateMapTheme(ctx, connect.NewRequest(input))
	require.NoError(t, err)
	service := auditedMapThemeServiceForTest(t, db, spiceDB, apitelemetry.NewDurableWriter(db))
	input.Name = " Snapshot source "
	input.LightVariant.BackgroundColor = " #ffffff "
	noop, changed, err := service.UpdateMapThemeSnapshot(ctx, created.Msg.Id, 1, input)
	require.NoError(t, err)
	require.False(t, changed)
	require.EqualValues(t, 1, noop.Revision)
	require.Equal(t, created.Msg.UpdatedAt, noop.UpdatedAt)
	var auditCount int64
	require.NoError(t, db.Table("domain_audit").Where("target_id = ?", created.Msg.Id).Count(&auditCount).Error)
	require.Zero(t, auditCount)
	input.Name = " Changed snapshot "
	input.Settings.ShowAreaLabels = false
	input.LightVariant.BuildingStrokeEnabled = false
	updated, changed, err := service.UpdateMapThemeSnapshot(ctx, created.Msg.Id, 1, input)
	require.NoError(t, err)
	require.True(t, changed)
	require.EqualValues(t, 2, updated.Revision)
	require.Equal(t, "Changed snapshot", updated.Name)
	require.False(t, updated.Settings.ShowAreaLabels)
	require.False(t, updated.LightVariant.BuildingStrokeEnabled)
	loaded, err := service.GetMapTheme(ctx, connect.NewRequest(&managev1.GetMapThemeRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	require.True(t, proto.Equal(updated, loaded.Msg))
	_, changed, err = service.UpdateMapThemeSnapshot(ctx, created.Msg.Id, 1, input)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.False(t, changed)
	var records []struct {
		Action        string
		ActorMemberID string `gorm:"column:actor_member_id"`
		RequestID     string `gorm:"column:request_id"`
	}
	require.NoError(t, db.Raw("SELECT action, actor_member_id::text AS actor_member_id, request_id::text AS request_id FROM domain_audit WHERE target_id = ?", created.Msg.Id).Scan(&records).Error)
	require.Len(t, records, 1)
	require.Equal(t, string(sharedtelemetry.AuditMapThemeUpdated), records[0].Action)
	require.Equal(t, admin.MemberID, records[0].ActorMemberID)
	require.Equal(t, sharedtelemetry.RequestIDFromContext(ctx), records[0].RequestID)
}

func TestMapThemeAdminSnapshotUpdateDenialAndAuditRollbackIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	spiceDB := integrationSpiceDB(t)
	admin := mapThemeAdminUser(t)
	ctx := withAuditedRequestContext(t, mapThemeMemberContext(admin))
	input := validCreateMapThemeRequest("Snapshot protected")
	created, err := mapThemeServiceForTest(t, db, spiceDB).CreateMapTheme(ctx, connect.NewRequest(input))
	require.NoError(t, err)
	service := auditedMapThemeServiceForTest(t, db, spiceDB, apitelemetry.NewDurableWriter(db))
	author := mapThemeAdminUser(t)
	grantIntegrationGlobalRole(t, spiceDB, author.IdentityID, policyv1.Role.Author())
	input.Name = "Unauthorized change"
	_, changed, err := service.UpdateMapThemeSnapshot(mapThemeMemberContext(author), created.Msg.Id, 1, input)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.False(t, changed)
	// Permission is checked before CAS: an Author does not learn conflict state.
	_, _, err = service.UpdateMapThemeSnapshot(mapThemeMemberContext(author), created.Msg.Id, 999, input)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	failing := auditedMapThemeServiceForTest(t, db, spiceDB, failingDomainAuditAppender{})
	_, changed, err = failing.UpdateMapThemeSnapshot(ctx, created.Msg.Id, 1, input)
	require.Error(t, err)
	require.False(t, changed)
	loaded, err := service.GetMapTheme(ctx, connect.NewRequest(&managev1.GetMapThemeRequest{Id: created.Msg.Id}))
	require.NoError(t, err)
	require.Equal(t, created.Msg.Name, loaded.Msg.Name)
	require.EqualValues(t, 1, loaded.Msg.Revision)
	require.Equal(t, created.Msg.UpdatedAt, loaded.Msg.UpdatedAt)
	var auditCount int64
	require.NoError(t, db.Table("domain_audit").Where("target_id = ?", created.Msg.Id).Count(&auditCount).Error)
	require.Zero(t, auditCount)
}
