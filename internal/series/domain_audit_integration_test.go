//go:build integration

package series

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	seriesadapter "github.com/echovisionlab/geul-api/internal/adapters/series"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type postSeriesAuditRow struct {
	Action        string
	TargetType    string `gorm:"column:target_type"`
	TargetID      string `gorm:"column:target_id"`
	ActorMemberID string `gorm:"column:actor_member_id"`
	RequestID     string `gorm:"column:request_id"`
	Attributes    []byte `gorm:"column:attributes"`
}

func TestPostSeriesDomainAuditMutationVariantsIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	managerID := integrationTestUUID()
	seedSeriesActor(t, db, adminID, "Series audit admin")
	seedSeriesActor(t, db, managerID, "Series audit manager")
	stack := testutil.SetupOryStack(t)
	adminSubject, err := auth.NewAccountIdentitySubject(auth.IdentityID(adminID))
	require.NoError(t, err)
	_, err = stack.SpiceDBClient.SyncAccountIdentityGlobalRole(t.Context(), adminSubject, policyv1.Role.Admin())
	require.NoError(t, err)
	service := auditedSeriesIntegrationService(t, db, adminID, stack.SpiceDBClient, apitelemetry.NewDurableWriter(db))
	ctx := seriesAuditContext(t, adminID)

	created, err := service.CreateSeries(ctx, connect.NewRequest(&managev1.CreateSeriesRequest{Title: "Series audit"}))
	require.NoError(t, err)
	seriesID := created.Msg.Id
	published := managev1.SeriesStatus_SERIES_STATUS_PUBLISHED.String()
	slug := "series-audit-updated"
	title := "Series audit source changed"
	_, err = service.UpdateSeries(ctx, connect.NewRequest(&managev1.UpdateSeriesRequest{
		Id: seriesID, Slug: &slug, Status: &published, Title: &title,
	}))
	require.NoError(t, err)

	managerMemberID := integrationMemberID(managerID)
	_, err = service.AddSeriesManager(ctx, connect.NewRequest(&managev1.AddSeriesManagerRequest{SeriesId: seriesID, MemberId: managerMemberID}))
	require.NoError(t, err)
	_, err = service.AddSeriesManager(ctx, connect.NewRequest(&managev1.AddSeriesManagerRequest{SeriesId: seriesID, MemberId: managerMemberID}))
	require.NoError(t, err)

	firstPostID := seedSeriesPost(t, db, integrationMemberID(adminID))
	secondPostID := seedSeriesPost(t, db, integrationMemberID(adminID))
	grantSeriesPostAuthorRelation(t, service, firstPostID, adminID)
	grantSeriesPostAuthorRelation(t, service, secondPostID, adminID)
	_, err = service.AssignPostToSeries(ctx, connect.NewRequest(&managev1.AssignPostToSeriesRequest{PostId: firstPostID, SeriesId: seriesID}))
	require.NoError(t, err)
	_, err = service.AssignPostToSeries(ctx, connect.NewRequest(&managev1.AssignPostToSeriesRequest{PostId: secondPostID, SeriesId: seriesID}))
	require.NoError(t, err)
	_, err = service.ReorderSeriesPosts(ctx, connect.NewRequest(&managev1.ReorderSeriesPostsRequest{SeriesId: seriesID, PostIds: []string{secondPostID, firstPostID}}))
	require.NoError(t, err)
	_, err = service.ReorderSeriesPosts(ctx, connect.NewRequest(&managev1.ReorderSeriesPostsRequest{SeriesId: seriesID, PostIds: []string{secondPostID, firstPostID}}))
	require.NoError(t, err)
	_, err = service.UnassignPostFromSeries(ctx, connect.NewRequest(&managev1.UnassignPostFromSeriesRequest{SeriesId: seriesID, PostId: firstPostID}))
	require.NoError(t, err)
	_, err = service.RemoveSeriesManager(ctx, connect.NewRequest(&managev1.RemoveSeriesManagerRequest{SeriesId: seriesID, MemberId: managerMemberID}))
	require.NoError(t, err)
	imageFileID := seedImageBindingUploadedFileFixture(t, db, "series/"+seriesID+"/featured.webp")
	_, err = service.SetSeriesFeaturedImage(ctx, connect.NewRequest(&managev1.SetSeriesFeaturedImageRequest{SeriesId: seriesID, FileId: imageFileID}))
	require.NoError(t, err)
	// The same File binding is a semantic no-op: no OG request or Audit row.
	_, err = service.SetSeriesFeaturedImage(ctx, connect.NewRequest(&managev1.SetSeriesFeaturedImageRequest{SeriesId: seriesID, FileId: imageFileID}))
	require.NoError(t, err)
	_, err = service.DeleteSeriesFeaturedImage(ctx, connect.NewRequest(&managev1.DeleteSeriesFeaturedImageRequest{SeriesId: seriesID}))
	require.NoError(t, err)
	_, err = service.DeleteSeries(ctx, connect.NewRequest(&managev1.DeleteSeriesRequest{Id: seriesID}))
	require.NoError(t, err)
	var retainedFileCount int64
	require.NoError(t, db.Table("file").Where("id = ?", imageFileID).Count(&retainedFileCount).Error)
	require.EqualValues(t, 1, retainedFileCount)

	rows := postSeriesAuditRows(t, db, seriesID)
	require.Len(t, rows, 12)
	wantActions := []string{
		"post_series.created", "post_series.updated", "post_series.updated", "post_series.updated",
		"post_series.updated", "post_series.updated", "post_series.updated", "post_series.updated", "post_series.updated",
		"post_series.updated", "post_series.updated", "post_series.deleted",
	}
	for index, row := range rows {
		require.Equal(t, wantActions[index], row.Action)
		require.Equal(t, "post_series", row.TargetType)
		require.Equal(t, seriesID, row.TargetID)
		require.Equal(t, integrationMemberID(adminID), row.ActorMemberID)
		require.Equal(t, sharedtelemetry.RequestIDFromContext(ctx), row.RequestID)
	}
	require.JSONEq(t, `{}`, string(rows[0].Attributes))
	require.JSONEq(t, `{"changed_fields":["slug","source_copy"]}`, string(rows[1].Attributes))
	require.JSONEq(t, `{"changed_fields":["status"],"previous_state":"draft","new_state":"published"}`, string(rows[2].Attributes))
	require.JSONEq(t, `{"changed_fields":["managers"],"subject_member_id":"`+managerMemberID+`","previous_relationship":"none","new_relationship":"manager"}`, string(rows[3].Attributes))
	require.JSONEq(t, `{"changed_fields":["posts"],"subject_post_id":"`+firstPostID+`","new_series_id":"`+seriesID+`"}`, string(rows[4].Attributes))
	require.JSONEq(t, `{"changed_fields":["posts"],"subject_post_id":"`+secondPostID+`","new_series_id":"`+seriesID+`"}`, string(rows[5].Attributes))
	require.JSONEq(t, `{"changed_fields":["post_order"],"post_ids":["`+secondPostID+`","`+firstPostID+`"]}`, string(rows[6].Attributes))
	require.JSONEq(t, `{"changed_fields":["posts"],"subject_post_id":"`+firstPostID+`","previous_series_id":"`+seriesID+`"}`, string(rows[7].Attributes))
	require.JSONEq(t, `{"changed_fields":["managers"],"subject_member_id":"`+managerMemberID+`","previous_relationship":"manager","new_relationship":"none"}`, string(rows[8].Attributes))
	require.JSONEq(t, `{"changed_fields":["featured_image"],"collection_operation":"added","file_id":"`+imageFileID+`"}`, string(rows[9].Attributes))
	require.JSONEq(t, `{"changed_fields":["featured_image"],"collection_operation":"removed","file_id":"`+imageFileID+`"}`, string(rows[10].Attributes))
	require.JSONEq(t, `{}`, string(rows[11].Attributes))
}

func TestPostSeriesAuditFailureRollsBackProductAndAuthorizationIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	managerID := integrationTestUUID()
	seedSeriesActor(t, db, adminID, "Series rollback admin")
	seedSeriesActor(t, db, managerID, "Series rollback manager")
	stack := testutil.SetupOryStack(t)
	adminSubject, err := auth.NewAccountIdentitySubject(auth.IdentityID(adminID))
	require.NoError(t, err)
	_, err = stack.SpiceDBClient.SyncAccountIdentityGlobalRole(t.Context(), adminSubject, policyv1.Role.Admin())
	require.NoError(t, err)
	ctx := seriesAuditContext(t, adminID)
	failing := auditedSeriesIntegrationService(t, db, adminID, stack.SpiceDBClient, seriesFailingAuditAppender{})
	_, err = failing.CreateSeries(ctx, connect.NewRequest(&managev1.CreateSeriesRequest{Title: "Must roll back"}))
	require.Error(t, err)
	var count int64
	require.NoError(t, db.Table("series").Where("slug = ?", "must-roll-back").Count(&count).Error)
	require.Zero(t, count)

	base := auditedSeriesIntegrationService(t, db, adminID, stack.SpiceDBClient, apitelemetry.NewDurableWriter(db))
	created, err := base.CreateSeries(ctx, connect.NewRequest(&managev1.CreateSeriesRequest{Title: "Manager rollback"}))
	require.NoError(t, err)
	managerMemberID := integrationMemberID(managerID)
	_, err = failing.AddSeriesManager(ctx, connect.NewRequest(&managev1.AddSeriesManagerRequest{SeriesId: created.Msg.Id, MemberId: managerMemberID}))
	require.Error(t, err)
	requireSeriesManagerRelation(t, base, managerID, created.Msg.Id, false)
	var managerCount int64
	require.NoError(t, db.Table("series_manager").Where("series_id = ? AND member_id = ?", created.Msg.Id, managerMemberID).Count(&managerCount).Error)
	require.Zero(t, managerCount)

	created, err = base.CreateSeries(ctx, connect.NewRequest(&managev1.CreateSeriesRequest{Title: "Delete rollback"}))
	require.NoError(t, err)
	_, err = base.AddSeriesManager(ctx, connect.NewRequest(&managev1.AddSeriesManagerRequest{SeriesId: created.Msg.Id, MemberId: managerMemberID}))
	require.NoError(t, err)
	requireSeriesManagerRelation(t, base, managerID, created.Msg.Id, true)
	_, err = failing.DeleteSeries(ctx, connect.NewRequest(&managev1.DeleteSeriesRequest{Id: created.Msg.Id}))
	require.Error(t, err)
	var deletedSeriesCount int64
	require.NoError(t, db.Table("series").Where("id = ?", created.Msg.Id).Count(&deletedSeriesCount).Error)
	require.EqualValues(t, 1, deletedSeriesCount)
	requireSeriesManagerRelation(t, base, managerID, created.Msg.Id, true)
}

type seriesFailingAuditAppender struct{}

func (seriesFailingAuditAppender) AppendDomainAuditInTransaction(context.Context, *gorm.DB, sharedtelemetry.AuditRecord) error {
	return errors.New("post series audit unavailable")
}

func auditedSeriesIntegrationService(t *testing.T, db *gorm.DB, adminID string, spiceDB *auth.SpiceDBClient, writer domainaudit.Appender) *SeriesService {
	t.Helper()
	runtime := seriesadapter.NewRuntime(db, "", newOGRefresherForTest(db, ""))
	return NewAuditedSeriesService(
		db,
		runtime,
		runtime,
		runtime,
		spiceDB,
		testutil.NewPostIdentityManager(testutil.PostIntegrationIdentity(adminID, "en")),
		seriesTestMenuTargets{},
		seriesTestPostAccess{},
		seriesTestMemberSummaries{},
		writer,
	)
}

func seriesAuditContext(t *testing.T, identityID string) context.Context {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.208")
	require.NoError(t, err)
	return auth.WithUser(sharedtelemetry.WithRequestContext(t.Context(), requestContext), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(integrationMemberID(identityID)),
		SessionID: auth.SessionID(integrationTestUUID()), Authenticated: true, Onboarded: true,
	})
}

func postSeriesAuditRows(t *testing.T, db *gorm.DB, seriesID string) []postSeriesAuditRow {
	t.Helper()
	var rows []postSeriesAuditRow
	require.NoError(t, db.Raw(`SELECT action, target_type, target_id, actor_member_id::text AS actor_member_id,
		request_id::text AS request_id, attributes FROM domain_audit
		WHERE target_type = 'post_series' AND target_id = ? ORDER BY occurred_at, audit_id`, seriesID).Scan(&rows).Error)
	return rows
}
