//go:build integration

package maptheme

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
)

func TestMapThemeCreateCopyDeleteAuditCommitsWithMutationIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	spiceDB := integrationSpiceDB(t)
	admin := mapThemeAdminUser(t)
	memberID := admin.MemberID
	service := auditedMapThemeServiceForTest(t, db, spiceDB, apitelemetry.NewDurableWriter(db))
	ctx := withAuditedRequestContext(t, mapThemeMemberContext(admin))

	created, err := service.CreateMapTheme(ctx, connect.NewRequest(validCreateMapThemeRequest("Audited source")))
	require.NoError(t, err)
	copied, err := service.CopyMapTheme(ctx, connect.NewRequest(&managev1.CopyMapThemeRequest{
		Id: created.Msg.Id, Name: "Audited copy",
	}))
	require.NoError(t, err)
	_, err = service.DeleteMapTheme(ctx, connect.NewRequest(&managev1.DeleteMapThemeRequest{Id: copied.Msg.Id}))
	require.NoError(t, err)

	var records []struct {
		Action        string
		TargetID      string `gorm:"column:target_id"`
		ActorMemberID string `gorm:"column:actor_member_id"`
		RequestID     string `gorm:"column:request_id"`
	}
	require.NoError(t, db.Raw(`
		SELECT action, target_id, actor_member_id::text AS actor_member_id, request_id::text AS request_id
		FROM public.domain_audit
		ORDER BY occurred_at ASC, audit_id ASC
	`).Scan(&records).Error)
	require.Len(t, records, 3)
	require.Equal(t, []string{
		string(sharedtelemetry.AuditMapThemeCreated),
		string(sharedtelemetry.AuditMapThemeCreated),
		string(sharedtelemetry.AuditMapThemeDeleted),
	}, []string{records[0].Action, records[1].Action, records[2].Action})
	require.Equal(t, []string{created.Msg.Id, copied.Msg.Id, copied.Msg.Id}, []string{records[0].TargetID, records[1].TargetID, records[2].TargetID})
	for _, record := range records {
		require.Equal(t, memberID, record.ActorMemberID)
		require.Equal(t, sharedtelemetry.RequestIDFromContext(ctx), record.RequestID)
	}
}

func TestMapThemeContentSaveAuditCommitsWithMutationIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	spiceDB := durableAudienceSpiceDB(t)
	admin := mapThemeAdminUser(t)
	created, err := mapThemeServiceForTest(t, db, spiceDB).CreateMapTheme(
		mapThemeMemberContext(admin),
		connect.NewRequest(validCreateMapThemeRequest("Audit content source")),
	)
	require.NoError(t, err)
	service := NewAuditedInternalMapService(db, apitelemetry.NewDurableWriter(db), spiceDB)
	ctx := withAuditedRequestContext(t, mapThemeMemberContext(admin))
	response, err := service.SaveMapThemeSnapshot(ctx, connect.NewRequest(&intrav1.SaveMapThemeSnapshotRequest{
		ThemeId: created.Msg.Id, Locale: "und", ExpectedRevision: 1,
		Snapshot: validDocumentSnapshot("Audited content"), ContributorMemberIds: []string{admin.MemberID},
	}))
	require.NoError(t, err)
	require.EqualValues(t, 2, response.Msg.Revision)
	require.Equal(t, "und", response.Msg.Locale)

	loaded, loadErr := NewInternalMapService(db, spiceDB).LoadMapThemeSnapshot(
		context.Background(), connect.NewRequest(&intrav1.LoadMapThemeSnapshotRequest{ThemeId: created.Msg.Id, Locale: "und"}),
	)
	require.NoError(t, loadErr)
	require.EqualValues(t, 2, loaded.Msg.Revision)
	require.Equal(t, "Audited content", loaded.Msg.Snapshot.Name)

	var records []struct {
		Action        string
		TargetType    string `gorm:"column:target_type"`
		TargetID      string `gorm:"column:target_id"`
		ActorMemberID string `gorm:"column:actor_member_id"`
		RequestID     string `gorm:"column:request_id"`
		Attributes    []byte
	}
	require.NoError(t, db.Raw(`
		SELECT action, target_type, target_id, actor_member_id::text AS actor_member_id,
		       request_id::text AS request_id, attributes
		FROM public.domain_audit
		WHERE target_type = 'map_theme' AND target_id = ?
	`, created.Msg.Id).Scan(&records).Error)
	require.Len(t, records, 1)
	require.Equal(t, string(sharedtelemetry.AuditMapThemeUpdated), records[0].Action)
	require.Equal(t, "map_theme", records[0].TargetType)
	require.Equal(t, created.Msg.Id, records[0].TargetID)
	require.Equal(t, admin.MemberID, records[0].ActorMemberID)
	require.Equal(t, sharedtelemetry.RequestIDFromContext(ctx), records[0].RequestID)
	var attributes map[string]any
	require.NoError(t, json.Unmarshal(records[0].Attributes, &attributes))
	require.Equal(t, map[string]any{"changed_fields": []any{"content"}}, attributes)
}

func TestMapThemeContentAuditAppendFailureRollsBackSaveIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	spiceDB := durableAudienceSpiceDB(t)
	admin := mapThemeAdminUser(t)
	created, err := mapThemeServiceForTest(t, db, spiceDB).CreateMapTheme(
		mapThemeMemberContext(admin),
		connect.NewRequest(validCreateMapThemeRequest("Rollback source")),
	)
	require.NoError(t, err)

	service := NewAuditedInternalMapService(db, failingDomainAuditAppender{}, spiceDB)
	_, err = service.SaveMapThemeSnapshot(
		withAuditedRequestContext(t, mapThemeMemberContext(admin)),
		connect.NewRequest(&intrav1.SaveMapThemeSnapshotRequest{
			ThemeId: created.Msg.Id, Locale: "und", ExpectedRevision: 1,
			Snapshot: validDocumentSnapshot("Must roll back"), ContributorMemberIds: []string{admin.MemberID},
		}),
	)
	require.Error(t, err)

	loaded, loadErr := NewInternalMapService(db, spiceDB).LoadMapThemeSnapshot(
		context.Background(), connect.NewRequest(&intrav1.LoadMapThemeSnapshotRequest{ThemeId: created.Msg.Id, Locale: "und"}),
	)
	require.NoError(t, loadErr)
	require.EqualValues(t, 1, loaded.Msg.Revision)
	require.Equal(t, created.Msg.Name, loaded.Msg.Snapshot.Name)
	var auditCount int64
	require.NoError(t, db.Table("public.domain_audit").Where("target_id = ?", created.Msg.Id).Count(&auditCount).Error)
	require.Zero(t, auditCount)
}

func TestMapThemeAuditAppendFailureRollsBackCreateIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	spiceDB := integrationSpiceDB(t)
	service := auditedMapThemeServiceForTest(t, db, spiceDB, failingDomainAuditAppender{})
	name := "Audit rollback"
	failingAdmin := mapThemeAdminUser(t)
	_, err := service.CreateMapTheme(
		mapThemeMemberContext(failingAdmin),
		connect.NewRequest(validCreateMapThemeRequest(name)),
	)
	require.Error(t, err)
	var count int64
	require.NoError(t, db.Table("public.map_theme").Where("name = ?", name).Count(&count).Error)
	require.Zero(t, count)
}
