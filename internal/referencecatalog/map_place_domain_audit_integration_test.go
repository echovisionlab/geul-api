//go:build integration

package referencecatalog_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type mapPlaceDomainAuditRecord struct {
	Action        string
	TargetType    string `gorm:"column:target_type"`
	TargetID      string `gorm:"column:target_id"`
	ActorMemberID string `gorm:"column:actor_member_id"`
	RequestID     string `gorm:"column:request_id"`
	Attributes    []byte `gorm:"column:attributes"`
}

func TestMapPlaceCurrentAuthorityRejectsStaleRequestPrivilegesIntegration(t *testing.T) {
	stack := testutil.PrepareOryIntegrationTest(t)
	db := stack.DB
	// The request is deliberately stamped Admin. Each call must instead use the
	// locked Identity/Member state that is current in its own transaction.
	ctx, identityID, _, spiceDB := mapPlaceAuditActor(t, stack, policyv1.Role.Admin())
	service := auditedMapPlaceServiceForTest(t, db, "https://cdn.example.com", spiceDB, apitelemetry.NewDurableWriter(db))

	setMapPlaceAuditActorRole(t, spiceDB, identityID, policyv1.Role.User())
	_, err := service.CreateMapPlace(ctx, connect.NewRequest(&managev1.CreateMapPlaceRequest{
		Name: "Rejected stale create", Address: "1 Authority Road", Lat: 37.5, Lng: 127.0,
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	setMapPlaceAuditActorRole(t, spiceDB, identityID, policyv1.Role.Author())
	created, err := service.CreateMapPlace(ctx, connect.NewRequest(&managev1.CreateMapPlaceRequest{
		Name: "Current authority place", Address: "2 Authority Road", Lat: 37.6, Lng: 127.1,
	}))
	require.NoError(t, err)

	setMapPlaceAuditActorRole(t, spiceDB, identityID, policyv1.Role.User())
	updatedName := "Rejected stale update"
	_, err = service.UpdateMapPlace(ctx, connect.NewRequest(&managev1.UpdateMapPlaceRequest{
		Id: created.Msg.Id, Name: &updatedName,
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.Equal(t, "Current authority place", readMapPlaceAuditRow(t, db, created.Msg.Id).Name)

	setMapPlaceAuditActorRole(t, spiceDB, identityID, policyv1.Role.Author())
	_, err = service.DeleteMapPlace(ctx, connect.NewRequest(&managev1.DeleteMapPlaceRequest{Id: created.Msg.Id}))
	require.Error(t, err)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.Equal(t, "Current authority place", readMapPlaceAuditRow(t, db, created.Msg.Id).Name)

	setMapPlaceAuditActorRole(t, spiceDB, identityID, policyv1.Role.Admin())
	require.NoError(t, db.Exec(`UPDATE kratos.identities SET state = 'inactive' WHERE id = ?::uuid`, identityID).Error)
	_, err = service.UpdateMapPlace(ctx, connect.NewRequest(&managev1.UpdateMapPlaceRequest{
		Id: created.Msg.Id, Name: &updatedName,
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.Equal(t, "Current authority place", readMapPlaceAuditRow(t, db, created.Msg.Id).Name)
}

func TestMapPlaceDomainAuditLifecycleAndImageVariantsIntegration(t *testing.T) {
	stack := testutil.PrepareOryIntegrationTest(t)
	db := stack.DB
	ctx, identityID, memberID, spiceDB := mapPlaceAuditActor(t, stack, policyv1.Role.Author())
	writer := apitelemetry.NewDurableWriter(db)
	service := auditedMapPlaceServiceForTest(t, db, "https://cdn.example.com", spiceDB, writer)

	firstFileID := testutil.IntegrationUUID()
	secondFileID := testutil.IntegrationUUID()
	firstAsset := seedMapImageSourceFixture(t, db, firstFileID)
	secondAsset := seedMapImageSourceFixture(t, db, secondFileID)
	created, err := service.CreateMapPlace(ctx, connect.NewRequest(&managev1.CreateMapPlaceRequest{
		Name: "Audited map place", Address: "1 Audit Road", Lat: 37.5, Lng: 127.0, ImageFileId: &firstFileID,
	}))
	require.NoError(t, err)

	name := "Audited map place updated"
	address := "2 Audit Road"
	updated, err := service.UpdateMapPlace(ctx, connect.NewRequest(&managev1.UpdateMapPlaceRequest{
		Id: created.Msg.Id, Name: &name, Address: &address, ImageFileId: &secondFileID,
	}))
	require.NoError(t, err)
	require.Equal(t, secondFileID, updated.Msg.GetImageFileId())
	requireMapPlaceAssetStatus(t, db, firstAsset.GetAssetId(), model.PublicAssetStatusReady)

	// Identical metadata and image are a semantic no-op: no timestamp,
	// attribution, public binding, or audit mutation is allowed.
	beforeNoOp := readMapPlaceAuditRow(t, db, created.Msg.Id)
	_, err = service.UpdateMapPlace(ctx, connect.NewRequest(&managev1.UpdateMapPlaceRequest{
		Id: created.Msg.Id, Name: &name, Address: &address, ImageFileId: &secondFileID,
	}))
	require.NoError(t, err)
	afterNoOp := readMapPlaceAuditRow(t, db, created.Msg.Id)
	require.Equal(t, beforeNoOp.UpdatedAt, afterNoOp.UpdatedAt)
	require.Equal(t, beforeNoOp.UpdatedByMemberID, afterNoOp.UpdatedByMemberID)

	_, err = service.UpdateMapPlace(ctx, connect.NewRequest(&managev1.UpdateMapPlaceRequest{
		Id: created.Msg.Id, ClearImage: true,
	}))
	require.NoError(t, err)
	requireMapPlaceAssetStatus(t, db, secondAsset.GetAssetId(), model.PublicAssetStatusReady)
	requireMapPlaceSourceFilesExist(t, db, firstFileID, secondFileID)

	// Delete is Admin-only and reads the current Identity role inside the TX.
	setMapPlaceAuditActorRole(t, spiceDB, identityID, policyv1.Role.Admin())
	_, err = service.DeleteMapPlace(ctx, connect.NewRequest(&managev1.DeleteMapPlaceRequest{Id: created.Msg.Id}))
	require.NoError(t, err)

	var records []mapPlaceDomainAuditRecord
	require.NoError(t, db.Raw(`
		SELECT action, target_type, target_id, actor_member_id::text AS actor_member_id,
		       request_id::text AS request_id, attributes
		FROM public.domain_audit
		WHERE target_type = 'map_place' AND target_id = ?
		ORDER BY occurred_at, audit_id
	`, created.Msg.Id).Scan(&records).Error)
	require.Len(t, records, 5)
	wantActions := []string{"map_place.created", "map_place.updated", "map_place.updated", "map_place.updated", "map_place.deleted"}
	wantAttributes := []string{
		`{}`,
		`{"changed_fields":["address","name"]}`,
		`{"changed_fields":["image"],"collection_operation":"added","file_id":"` + secondFileID + `"}`,
		`{"changed_fields":["image"],"collection_operation":"removed","file_id":"` + secondFileID + `"}`,
		`{}`,
	}
	for index, record := range records {
		require.Equal(t, wantActions[index], record.Action)
		require.Equal(t, "map_place", record.TargetType)
		require.Equal(t, created.Msg.Id, record.TargetID)
		require.Equal(t, memberID, record.ActorMemberID)
		require.Equal(t, sharedtelemetry.RequestIDFromContext(ctx), record.RequestID)
		require.JSONEq(t, wantAttributes[index], string(record.Attributes))
	}
}

func TestMapPlaceAuditRollbackAndReferenceIntegrityIntegration(t *testing.T) {
	stack := testutil.PrepareOryIntegrationTest(t)
	db := stack.DB
	ctx, _, _, spiceDB := mapPlaceAuditActor(t, stack, policyv1.Role.Admin())
	base := mapPlaceServiceForTest(t, db, "https://cdn.example.com", spiceDB)

	name := "Map Place Audit Rollback"
	_, err := auditedMapPlaceServiceForTest(t, db, "https://cdn.example.com", spiceDB, mapPlaceFailingAuditAppender{}).CreateMapPlace(ctx, connect.NewRequest(&managev1.CreateMapPlaceRequest{
		Name: name, Address: "1 Rollback Road", Lat: 37.5, Lng: 127.0,
	}))
	require.Error(t, err)
	var failedCreateCount int64
	require.NoError(t, db.Table("map_place").Where("name = ?", name).Count(&failedCreateCount).Error)
	require.Zero(t, failedCreateCount)

	rollbackUpdate, err := base.CreateMapPlace(ctx, connect.NewRequest(&managev1.CreateMapPlaceRequest{
		Name: "Map Place Update Rollback", Address: "Update Rollback Road", Lat: 37.4, Lng: 127.2,
	}))
	require.NoError(t, err)
	updatedName := "must not commit"
	_, err = auditedMapPlaceServiceForTest(t, db, "https://cdn.example.com", spiceDB, mapPlaceFailingAuditAppender{}).UpdateMapPlace(ctx, connect.NewRequest(&managev1.UpdateMapPlaceRequest{
		Id: rollbackUpdate.Msg.Id, Name: &updatedName,
	}))
	require.Error(t, err)
	require.Equal(t, "Map Place Update Rollback", readMapPlaceAuditRow(t, db, rollbackUpdate.Msg.Id).Name)

	rollbackDelete, err := base.CreateMapPlace(ctx, connect.NewRequest(&managev1.CreateMapPlaceRequest{
		Name: "Map Place Delete Rollback", Address: "Delete Rollback Road", Lat: 37.3, Lng: 127.3,
	}))
	require.NoError(t, err)
	_, err = auditedMapPlaceServiceForTest(t, db, "https://cdn.example.com", spiceDB, mapPlaceFailingAuditAppender{}).DeleteMapPlace(ctx, connect.NewRequest(&managev1.DeleteMapPlaceRequest{Id: rollbackDelete.Msg.Id}))
	require.Error(t, err)
	var rollbackDeleteCount int64
	require.NoError(t, db.Table("map_place").Where("id = ?", rollbackDelete.Msg.Id).Count(&rollbackDeleteCount).Error)
	require.EqualValues(t, 1, rollbackDeleteCount)

	place, err := base.CreateMapPlace(ctx, connect.NewRequest(&managev1.CreateMapPlaceRequest{
		Name: "Map Place Reference Guard", Address: "2 Rollback Road", Lat: 37.6, Lng: 127.1,
	}))
	require.NoError(t, err)
	workID := uuid.NewString()
	workDocumentID := seedMapPlaceReferenceDocument(t, db, "work")
	require.NoError(t, db.Exec(`
		INSERT INTO public.work (id, type, year, month, until_year, until_month, map_place_id, content_document_id)
		VALUES (?::uuid, 'WORK_TYPE_PORTFOLIO', 2026, 1, 2026, 1, ?::uuid, ?::uuid)
	`, workID, place.Msg.Id, workDocumentID).Error)

	audited := auditedMapPlaceServiceForTest(t, db, "https://cdn.example.com", spiceDB, apitelemetry.NewDurableWriter(db))
	_, err = audited.DeleteMapPlace(ctx, connect.NewRequest(&managev1.DeleteMapPlaceRequest{Id: place.Msg.Id}))
	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	var retainedPlaceCount int64
	require.NoError(t, db.Table("map_place").Where("id = ?", place.Msg.Id).Count(&retainedPlaceCount).Error)
	require.EqualValues(t, 1, retainedPlaceCount)
	var deleteAuditCount int64
	require.NoError(t, db.Table("domain_audit").Where("target_type = 'map_place' AND target_id = ?", place.Msg.Id).Count(&deleteAuditCount).Error)
	require.Zero(t, deleteAuditCount)

	// The migration is the final integrity boundary even if a future writer
	// misses the service preflight.
	require.NoError(t, db.SavePoint("map_place_reference_fk_guard").Error)
	require.Error(t, db.Exec(`DELETE FROM public.map_place WHERE id = ?::uuid`, place.Msg.Id).Error)
	require.NoError(t, db.RollbackTo("map_place_reference_fk_guard").Error)

	postPlace := createMapPlaceReferenceGuard(t, base, ctx, "Post")
	postDocumentID := seedMapPlaceReferenceDocument(t, db, "post")
	require.NoError(t, db.Exec(
		`INSERT INTO public.post (id, map_place_id, content_document_id) VALUES (?::uuid, ?::uuid, ?::uuid)`,
		uuid.NewString(), postPlace.Msg.Id, postDocumentID,
	).Error)
	assertMapPlaceInUseDeleteRejected(t, audited, ctx, postPlace.Msg.Id)

	programPlace := createMapPlaceReferenceGuard(t, base, ctx, "Program")
	typeID := uuid.NewString()
	programDocumentID := seedMapPlaceReferenceDocument(t, db, "program_event")
	require.NoError(t, db.Exec(`INSERT INTO public.program_event_type (id, slug) VALUES (?::uuid, ?)`, typeID, "map-place-audit-type-"+uuid.NewString()).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO public.program_event (id, slug, type_id, starts_at, map_place_id, content_document_id)
		VALUES (?::uuid, ?, ?::uuid, NOW(), ?::uuid, ?::uuid)
	`, uuid.NewString(), "map-place-audit-event-"+uuid.NewString(), typeID, programPlace.Msg.Id, programDocumentID).Error)
	assertMapPlaceInUseDeleteRejected(t, audited, ctx, programPlace.Msg.Id)
}

type mapPlaceFailingAuditAppender struct{}

func (mapPlaceFailingAuditAppender) AppendDomainAuditInTransaction(context.Context, *gorm.DB, sharedtelemetry.AuditRecord) error {
	return errors.New("map place audit unavailable")
}

func seedMapPlaceReferenceDocument(t *testing.T, db *gorm.DB, profile string) string {
	t.Helper()
	documentID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO public.content_document (id, profile) VALUES (?::uuid, ?)`, documentID, profile,
	).Error)
	return documentID
}

func mapPlaceAuditActor(t *testing.T, stack *testutil.OryStack, role policyv1.RoleID) (context.Context, string, string, *auth.SpiceDBClient) {
	t.Helper()
	user := stack.CreateUser(t, role.ID())
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.77")
	require.NoError(t, err)
	ctx := sharedtelemetry.WithRequestContext(t.Context(), requestContext)
	return auth.WithUser(ctx, user.AuthUserInfo()), user.IdentityID, user.MemberID, stack.SpiceDBClient
}

func setMapPlaceAuditActorRole(t *testing.T, spiceDB *auth.SpiceDBClient, identityID string, role policyv1.RoleID) {
	t.Helper()
	testutil.GrantIntegrationGlobalRole(t, spiceDB, identityID, role)
}

func readMapPlaceAuditRow(t *testing.T, db *gorm.DB, placeID string) model.MapPlace {
	t.Helper()
	var place model.MapPlace
	require.NoError(t, db.First(&place, "id = ?", placeID).Error)
	return place
}

func requireMapPlaceSourceFilesExist(t *testing.T, db *gorm.DB, fileIDs ...string) {
	t.Helper()
	var count int64
	require.NoError(t, db.Table("file").Where("id IN ?", fileIDs).Count(&count).Error)
	require.EqualValues(t, len(fileIDs), count)
}

func createMapPlaceReferenceGuard(t *testing.T, service *referencecatalog.MapPlaceService, ctx context.Context, suffix string) *connect.Response[managev1.MapPlace] {
	t.Helper()
	place, err := service.CreateMapPlace(ctx, connect.NewRequest(&managev1.CreateMapPlaceRequest{
		Name: "Map Place " + suffix + " Reference Guard", Address: suffix + " Reference Road", Lat: 37.7, Lng: 127.2,
	}))
	require.NoError(t, err)
	return place
}

func assertMapPlaceInUseDeleteRejected(t *testing.T, service *referencecatalog.MapPlaceService, ctx context.Context, placeID string) {
	t.Helper()
	_, err := service.DeleteMapPlace(ctx, connect.NewRequest(&managev1.DeleteMapPlaceRequest{Id: placeID}))
	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}
