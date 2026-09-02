//go:build integration

package filemedia

import (
	"context"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestAuthorizeManageFileDeliveriesRequiresIndependentFileUploader(t *testing.T) {
	t.Parallel()
	db := newFileDeliveryAuthorizationDB(t)
	stack := testutil.SetupOryStack(t)
	manager := stack.CreateUser(t, policyv1.Role.User().ID())
	other := stack.CreateUser(t, policyv1.Role.User().ID())
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	svc := &FileService{db: db, spiceDB: stack.SpiceDBClient, postAccess: newIntegrationPostAccess(db, stack.SpiceDBClient), programEventAttachment: newIntegrationProgramEventAccess(db)}
	for _, uploadType := range []managev1.UploadType{
		managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO,
	} {
		fileID := uuid.NewString()
		seedFileDeliveryBinding(t, db, fileID, uploadType, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED, "", manager.MemberID)
		require.NoError(t, svc.authorizeManageFileDeliveries(fileDeliveryPrincipalContext(manager), []string{fileID}))
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(svc.authorizeManageFileDeliveries(fileDeliveryPrincipalContext(other), []string{fileID})))
		require.NoError(t, svc.authorizeManageFileDeliveries(fileDeliveryPrincipalContext(admin), []string{fileID}))
	}
}

func TestAuthorizeManageFileDeliveriesFailsClosedForMissingOrMixedBindings(t *testing.T) {
	t.Parallel()
	db := newFileDeliveryAuthorizationDB(t)
	stack := testutil.SetupOryStack(t)
	owner := stack.CreateUser(t, policyv1.Role.User().ID())
	ownedFileID, missingFileID := uuid.NewString(), uuid.NewString()
	seedFileDeliveryBinding(t, db, ownedFileID, managev1.UploadType_UPLOAD_TYPE_USER_AVATAR, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED, owner.MemberID)
	svc := &FileService{db: db, spiceDB: stack.SpiceDBClient, postAccess: newIntegrationPostAccess(db, stack.SpiceDBClient), programEventAttachment: newIntegrationProgramEventAccess(db)}
	ctx := fileDeliveryPrincipalContext(owner)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(svc.authorizeManageFileDeliveries(ctx, []string{missingFileID})))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(svc.authorizeManageFileDeliveries(ctx, []string{ownedFileID, missingFileID})))
}

func TestAuthorizeManageFileDeliveriesSupportsOwnerAndAdminBoundaries(t *testing.T) {
	t.Parallel()
	db := newFileDeliveryAuthorizationDB(t)
	stack := testutil.SetupOryStack(t)
	avatarOwner := stack.CreateUser(t, policyv1.Role.User().ID())
	otherUser := stack.CreateUser(t, policyv1.Role.User().ID())
	ordinaryUser := stack.CreateUser(t, policyv1.Role.User().ID())
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	avatarFileID, mapFileID, mapEntityID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	seedFileDeliveryBinding(t, db, avatarFileID, managev1.UploadType_UPLOAD_TYPE_USER_AVATAR, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED, avatarOwner.MemberID)
	require.NoError(t, db.Exec(`CREATE TABLE map_place (id TEXT PRIMARY KEY)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO map_place (id) VALUES (?)`, mapEntityID).Error)
	seedFileDeliveryBinding(t, db, mapFileID, managev1.UploadType_UPLOAD_TYPE_MAP_IMAGE, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED, mapEntityID)
	svc := &FileService{db: db, spiceDB: stack.SpiceDBClient, postAccess: newIntegrationPostAccess(db, stack.SpiceDBClient), programEventAttachment: newIntegrationProgramEventAccess(db)}
	require.NoError(t, svc.authorizeManageFileDeliveries(fileDeliveryPrincipalContext(avatarOwner), []string{avatarFileID}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(svc.authorizeManageFileDeliveries(fileDeliveryPrincipalContext(otherUser), []string{avatarFileID})))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(svc.authorizeManageFileDeliveries(fileDeliveryPrincipalContext(ordinaryUser), []string{mapFileID})))
	require.NoError(t, svc.authorizeManageFileDeliveries(fileDeliveryPrincipalContext(admin), []string{avatarFileID, mapFileID}))
}

func TestAuthorizeManageFileDeliveriesUsesAvatarTargetOwnerNotUploader(t *testing.T) {
	t.Parallel()
	db := newFileDeliveryAuthorizationDB(t)
	stack := testutil.SetupOryStack(t)
	avatarOwner := stack.CreateUser(t, policyv1.Role.User().ID())
	adminUploader := stack.CreateUser(t, policyv1.Role.Admin().ID())
	fileID := uuid.NewString()
	seedFileDeliveryBinding(
		t, db, fileID, managev1.UploadType_UPLOAD_TYPE_USER_AVATAR,
		managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED,
		avatarOwner.MemberID, adminUploader.MemberID,
	)
	svc := &FileService{db: db, spiceDB: stack.SpiceDBClient, postAccess: newIntegrationPostAccess(db, stack.SpiceDBClient), programEventAttachment: newIntegrationProgramEventAccess(db)}
	authorization, err := svc.authorizeManageFileDeliveriesWithWitness(fileDeliveryPrincipalContext(avatarOwner), []string{fileID})
	require.NoError(t, err)
	require.Equal(t, avatarOwner.MemberID, authorization.files[fileID].expectedAvatarOwner)
	require.Empty(t, authorization.files[fileID].expectedUploader)
}

func TestManageMediaDeliveryRejectsArbitraryAndMixedFileIDsBeforeSigning(t *testing.T) {
	t.Parallel()
	db := newFileDeliveryAuthorizationDB(t)
	stack := testutil.SetupOryStack(t)
	owner := stack.CreateUser(t, policyv1.Role.User().ID())
	ownedFileID, arbitraryFileID := uuid.NewString(), uuid.NewString()
	seedFileDeliveryBinding(t, db, ownedFileID, managev1.UploadType_UPLOAD_TYPE_USER_AVATAR, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED, owner.MemberID)
	svc := &FileService{db: db, spiceDB: stack.SpiceDBClient, postAccess: newIntegrationPostAccess(db, stack.SpiceDBClient), programEventAttachment: newIntegrationProgramEventAccess(db)}
	ctx := fileDeliveryPrincipalContext(owner)
	_, err := svc.GetMediaDelivery(ctx, connect.NewRequest(&managev1.GetMediaDeliveryRequest{FileId: arbitraryFileID}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	_, err = svc.GetBulkMediaDeliveries(ctx, connect.NewRequest(&managev1.GetBulkMediaDeliveriesRequest{FileIds: []string{ownedFileID, arbitraryFileID}}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func TestManageBulkMediaDeliveryRejectsOversizedRequestUnit(t *testing.T) {
	t.Parallel()
	fileIDs := make([]string, MaxMediaDeliveryBatchSize+1)
	for index := range fileIDs {
		fileIDs[index] = uuid.NewString()
	}
	_, err := (&FileService{}).GetBulkMediaDeliveries(context.Background(), connect.NewRequest(&managev1.GetBulkMediaDeliveriesRequest{FileIds: fileIDs}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestAuthorizeManageFileDeliveriesRejectsUnauthenticatedAndBannedUsers(t *testing.T) {
	t.Parallel()
	db := newFileDeliveryAuthorizationDB(t)
	stack := testutil.SetupOryStack(t)
	owner := stack.CreateUser(t, policyv1.Role.User().ID())
	fileID := uuid.NewString()
	seedFileDeliveryBinding(t, db, fileID, managev1.UploadType_UPLOAD_TYPE_USER_AVATAR, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED, owner.MemberID)
	svc := &FileService{db: db, spiceDB: stack.SpiceDBClient, postAccess: newIntegrationPostAccess(db, stack.SpiceDBClient), programEventAttachment: newIntegrationProgramEventAccess(db)}
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(svc.authorizeManageFileDeliveries(context.Background(), []string{fileID})))
	banned := auth.WithUser(context.Background(), &auth.UserInfo{IdentityID: auth.IdentityID(owner.IdentityID), MemberID: auth.MemberID(owner.MemberID), Authenticated: true, Banned: true})
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(svc.authorizeManageFileDeliveries(banned, []string{fileID})))
}

func TestAuthorizeManageFileDeliveriesDeduplicatesEntityPermissionChecks(t *testing.T) {
	t.Parallel()
	db := newFileDeliveryAuthorizationDB(t)
	stack := testutil.SetupOryStack(t)
	manager := stack.CreateUser(t, policyv1.Role.User().ID())
	fileIDs := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
	postID := seedCurrentPostFileDelivery(t, db, fileIDs)
	seedFileDeliveryContentPolicy(t, stack.SpiceDBClient, "post", postID)
	seedFileDeliveryPostCollaboratorAuthority(t, stack.SpiceDBClient, postID, manager.IdentityID)
	svc := &FileService{db: db, spiceDB: stack.SpiceDBClient, postAccess: newIntegrationPostAccess(db, stack.SpiceDBClient), programEventAttachment: newIntegrationProgramEventAccess(db)}
	require.NoError(t, svc.authorizeManageFileDeliveries(fileDeliveryPrincipalContext(manager), fileIDs))
}

func TestAuthorizeManageFileDeliveriesUsesCurrentPostAttachmentsNotIngestBinding(t *testing.T) {
	t.Parallel()
	db := newFileDeliveryAuthorizationDB(t)
	stack := testutil.SetupOryStack(t)
	manager := stack.CreateUser(t, policyv1.Role.User().ID())
	otherAuthor := stack.CreateUser(t, policyv1.Role.User().ID())
	fileIDs := []string{uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()}
	postID := seedCurrentPostFileDelivery(t, db, fileIDs)
	seedFileDeliveryContentPolicy(t, stack.SpiceDBClient, "post", postID)
	seedFileDeliveryPostCollaboratorAuthority(t, stack.SpiceDBClient, postID, manager.IdentityID)

	unrelatedFileID := uuid.NewString()
	seedCurrentPostFileDelivery(t, db, []string{unrelatedFileID})
	detachedFileID := uuid.NewString()
	legacyPostEntityType := "POST"
	require.NoError(t, db.Exec(`
		INSERT INTO file (id, file_name, extension, mime_type, file_size)
		VALUES (?, 'detached.mp4', 'mp4', 'video/mp4', 4096)
	`, detachedFileID).Error)
	require.NoError(t, db.Create(&model.FileIngestBinding{
		FileID: detachedFileID, UploadType: managev1.UploadType_UPLOAD_TYPE_FEATURED_IMAGE.String(),
		EntityType: &legacyPostEntityType, EntityID: postID,
	}).Error)

	svc := &FileService{db: db, spiceDB: stack.SpiceDBClient, postAccess: newIntegrationPostAccess(db, stack.SpiceDBClient), programEventAttachment: newIntegrationProgramEventAccess(db)}
	managerCtx := fileDeliveryPrincipalContext(manager)
	require.NoError(t, svc.authorizeManageFileDeliveries(managerCtx, fileIDs))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(
		svc.authorizeManageFileDeliveries(managerCtx, []string{unrelatedFileID}),
	))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(
		svc.authorizeManageFileDeliveries(managerCtx, []string{detachedFileID}),
	))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(
		svc.authorizeManageFileDeliveries(fileDeliveryPrincipalContext(otherAuthor), []string{fileIDs[0]}),
	))
}

func TestArchivedPostFileBoundariesAllowAuthorViewAndRequireAdminEdit(t *testing.T) {
	db := newFileDeliveryAuthorizationDB(t)
	stack := testutil.SetupOryStack(t)
	author := stack.CreateUser(t, policyv1.Role.Author().ID())
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	fileID := uuid.NewString()
	postID := seedCurrentPostFileDelivery(t, db, []string{fileID})
	require.NoError(t, db.Table("post").Where("id = ?", postID).
		Update("status", managev1.PostStatus_POST_STATUS_ARCHIVED.String()).Error)
	seedFileDeliveryContentPolicy(t, stack.SpiceDBClient, "post", postID)
	seedFileDeliveryPostAuthority(t, stack.SpiceDBClient, postID, author.IdentityID)

	service := &FileService{
		db: db, spiceDB: stack.SpiceDBClient,
		postAccess: newIntegrationPostAccess(db, stack.SpiceDBClient),
	}
	authorCtx := fileDeliveryPrincipalContext(author)
	adminCtx := fileDeliveryPrincipalContext(admin)

	require.NoError(t, service.authorizeManageFileDeliveries(authorCtx, []string{fileID}))
	require.NoError(t, service.authorizeFileDownloadPolicyOwner(
		authorCtx,
		db,
		fileDownloadPolicyRelation{ResourceType: "post", ResourceID: postID},
		filePolicyOwnerView,
	))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(
		service.authorizeFileDownloadPolicyOwner(
			authorCtx,
			db,
			fileDownloadPolicyRelation{ResourceType: "post", ResourceID: postID},
			filePolicyOwnerEdit,
		),
	))
	require.NoError(t, service.authorizeFileDownloadPolicyOwner(
		adminCtx,
		db,
		fileDownloadPolicyRelation{ResourceType: "post", ResourceID: postID},
		filePolicyOwnerEdit,
	))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(service.checkEntityPermission(
		authorCtx,
		managev1.UploadType_UPLOAD_TYPE_FEATURED_IMAGE,
		"",
		postID,
		author.MemberID,
	)))
	require.NoError(t, service.checkEntityPermission(
		adminCtx,
		managev1.UploadType_UPLOAD_TYPE_FEATURED_IMAGE,
		"",
		postID,
		admin.MemberID,
	))

	require.Equal(t, connect.CodeNotFound, connect.CodeOf(requireFreshFileIngestAuthority(
		authorCtx,
		db,
		service,
		managev1.UploadType_UPLOAD_TYPE_FEATURED_IMAGE,
		managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED,
		postID,
	)))
	require.NoError(t, requireFreshFileIngestAuthority(
		adminCtx,
		db,
		service,
		managev1.UploadType_UPLOAD_TYPE_FEATURED_IMAGE,
		managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED,
		postID,
	))
}

func TestArchivedProgramEventFileBoundariesAllowAuthorViewAndRequireAdminEdit(t *testing.T) {
	db := newFileDeliveryAuthorizationDB(t)
	stack := testutil.SetupOryStack(t)
	author := stack.CreateUser(t, policyv1.Role.Author().ID())
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	fileID := uuid.NewString()
	eventID := seedCurrentContentFileDelivery(t, db, "program_event", []string{fileID}, false)
	require.NoError(t, db.Table("program_event").Where("id = ?", eventID).
		Update("status", managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED.String()).Error)
	seedFileDeliveryContentPolicy(t, stack.SpiceDBClient, "program_event", eventID)

	service := &FileService{
		db: db, spiceDB: stack.SpiceDBClient,
		programEventAttachment: newIntegrationProgramEventAccess(db),
	}
	authorCtx := fileDeliveryPrincipalContext(author)
	adminCtx := fileDeliveryPrincipalContext(admin)

	require.NoError(t, service.authorizeManageFileDeliveries(authorCtx, []string{fileID}))
	require.NoError(t, service.authorizeFileDownloadPolicyOwner(
		authorCtx,
		db,
		fileDownloadPolicyRelation{ResourceType: "program_event", ResourceID: eventID},
		filePolicyOwnerView,
	))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(
		service.authorizeFileDownloadPolicyOwner(
			authorCtx,
			db,
			fileDownloadPolicyRelation{ResourceType: "program_event", ResourceID: eventID},
			filePolicyOwnerEdit,
		),
	))
	require.NoError(t, service.authorizeFileDownloadPolicyOwner(
		adminCtx,
		db,
		fileDownloadPolicyRelation{ResourceType: "program_event", ResourceID: eventID},
		filePolicyOwnerEdit,
	))
}

func TestAuthorizeManageFileDeliveriesUsesCurrentContentAttachmentsNotIngestBinding(t *testing.T) {
	db := newFileDeliveryAuthorizationDB(t)
	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	other := stack.CreateUser(t, policyv1.Role.User().ID())
	svc := &FileService{db: db, spiceDB: stack.SpiceDBClient, postAccess: newIntegrationPostAccess(db, stack.SpiceDBClient), programEventAttachment: newIntegrationProgramEventAccess(db)}

	tests := []struct {
		name           string
		table          string
		resourceType   string
		featured       bool
		legacyEntityID string
	}{
		{name: "Post block attachment", table: "post", resourceType: "post", legacyEntityID: "POST"},
		{name: "Post featured image", table: "post", resourceType: "post", featured: true, legacyEntityID: "POST"},
		{name: "Page block attachment", table: "page", resourceType: "page", legacyEntityID: "PAGE"},
		{name: "Page featured image", table: "page", resourceType: "page", featured: true, legacyEntityID: "PAGE"},
		{name: "Work block attachment", table: "work", resourceType: "work", legacyEntityID: "WORK"},
		{name: "Work featured image", table: "work", resourceType: "work", featured: true, legacyEntityID: "WORK"},
		{name: "Program Event block attachment", table: "program_event", resourceType: "program_event", legacyEntityID: "PROGRAM_EVENT"},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fileID := uuid.NewString()
			entityID := seedCurrentContentFileDelivery(t, db, testCase.table, []string{fileID}, testCase.featured)
			seedFileDeliveryContentPolicy(t, stack.SpiceDBClient, testCase.resourceType, entityID)

			adminCtx := fileDeliveryPrincipalContext(admin)
			require.NoError(t, svc.authorizeManageFileDeliveries(adminCtx, []string{fileID}))

			unrelatedFileID := uuid.NewString()
			seedCurrentContentFileDelivery(t, db, testCase.table, []string{unrelatedFileID}, testCase.featured)
			require.NoError(t, svc.authorizeManageFileDeliveries(adminCtx, []string{unrelatedFileID}))

			detachedFileID := uuid.NewString()
			require.NoError(t, db.Exec(`INSERT INTO file (id) VALUES (?)`, detachedFileID).Error)
			legacyEntityType := testCase.legacyEntityID
			require.NoError(t, db.Create(&model.FileIngestBinding{
				FileID: detachedFileID, UploadType: managev1.UploadType_UPLOAD_TYPE_FEATURED_IMAGE.String(),
				EntityType: &legacyEntityType, EntityID: entityID,
			}).Error)
			require.NoError(t, svc.authorizeManageFileDeliveries(adminCtx, []string{detachedFileID}))
			require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(
				svc.authorizeManageFileDeliveries(fileDeliveryPrincipalContext(other), []string{detachedFileID}),
			))
		})
	}
}

func TestAuthorizeManageFileDeliveriesGlobalAuthorIsIndependentOfCurrentContentOwners(t *testing.T) {
	db := newFileDeliveryAuthorizationDB(t)
	stack := testutil.SetupOryStack(t)
	manager := stack.CreateUser(t, policyv1.Role.Author().ID())
	fileID := uuid.NewString()
	firstPostID := seedCurrentPostFileDelivery(t, db, []string{fileID})
	seedFileDeliveryContentPolicy(t, stack.SpiceDBClient, "post", firstPostID)
	seedFileDeliveryPostAuthority(t, stack.SpiceDBClient, firstPostID, manager.IdentityID)

	secondPostID := uuid.NewString()
	secondDocumentID := uuid.NewString()
	secondBlockID := uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO content_document (id) VALUES (?)`, secondDocumentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO post (id, content_document_id) VALUES (?, ?)`, secondPostID, secondDocumentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO content_block (id, document_id) VALUES (?, ?)`, secondBlockID, secondDocumentID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO content_block_attachment (block_id, reference_path, selector_kind, file_id)
		VALUES (?, 'shared-file', 'active', ?)
	`, secondBlockID, fileID).Error)

	svc := &FileService{db: db, spiceDB: stack.SpiceDBClient, postAccess: newIntegrationPostAccess(db, stack.SpiceDBClient), programEventAttachment: newIntegrationProgramEventAccess(db)}
	require.NoError(t, svc.authorizeManageFileDeliveries(fileDeliveryPrincipalContext(manager), []string{fileID}))
}

func TestAuthorizeManageFileDeliveriesUsesCurrentPostAttachmentCurrentSchemaIntegration(t *testing.T) {
	pg := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{
		BootstrapKratosStub: true,
		ApplyAppSchemaSQL:   true,
	})
	postID, fileID := seedCurrentSchemaPostFileDelivery(t, pg.DB)

	svc := &FileService{db: pg.DB}
	usages, err := svc.currentManageFileDeliveryUsages(t.Context(), []string{fileID})
	require.NoError(t, err)
	require.Len(t, usages[fileID], 1)
	require.Equal(t, "content_attachment", usages[fileID][0].kind)
	require.Equal(t, "post", usages[fileID][0].resourceType)
	require.Equal(t, postID, usages[fileID][0].resourceID)

	var bindingCount int64
	require.NoError(t, pg.DB.Table("file_ingest_binding").Where("file_id = ?", fileID).Count(&bindingCount).Error)
	require.Zero(t, bindingCount)
}

func TestAuthorizeManageFileDeliveriesRejectsPendingDeleteFile(t *testing.T) {
	t.Parallel()
	db := newFileDeliveryAuthorizationDB(t)
	stack := testutil.SetupOryStack(t)
	uploader := stack.CreateUser(t, policyv1.Role.Author().ID())
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	fileID := uuid.NewString()
	seedFileDeliveryBinding(t, db, fileID, managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED, "", uploader.MemberID)
	require.NoError(t, db.Exec(`UPDATE file SET delete_requested_at = CURRENT_TIMESTAMP WHERE id = ?`, fileID).Error)
	svc := &FileService{db: db, spiceDB: stack.SpiceDBClient, postAccess: newIntegrationPostAccess(db, stack.SpiceDBClient), programEventAttachment: newIntegrationProgramEventAccess(db)}
	for _, principal := range []*testutil.OryUser{uploader, admin} {
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(
			svc.authorizeManageFileDeliveries(fileDeliveryPrincipalContext(principal), []string{fileID}),
		))
	}
}

func TestAuthorizeManageFileDeliveriesAllowsGlobalLibraryAuthorForTrackAudio(t *testing.T) {
	t.Parallel()
	db := newFileDeliveryAuthorizationDB(t)
	stack := testutil.SetupOryStack(t)
	author := stack.CreateUser(t, policyv1.Role.Author().ID())
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	releaseID := uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO release (id) VALUES (?)`, releaseID).Error)
	fileIDs := []string{uuid.NewString(), uuid.NewString()}
	for _, fileID := range fileIDs {
		trackID := uuid.NewString()
		require.NoError(t, db.Exec(`INSERT INTO track (id, release_id) VALUES (?, ?)`, trackID, releaseID).Error)
		seedFileDeliveryBinding(t, db, fileID, managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED, trackID)
	}
	svc := &FileService{db: db, spiceDB: stack.SpiceDBClient, postAccess: newIntegrationPostAccess(db, stack.SpiceDBClient), programEventAttachment: newIntegrationProgramEventAccess(db)}
	require.NoError(t, svc.authorizeManageFileDeliveries(fileDeliveryPrincipalContext(author), fileIDs))
	require.NoError(t, svc.authorizeManageFileDeliveries(fileDeliveryPrincipalContext(admin), fileIDs))
}

func newFileDeliveryAuthorizationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE file_ingest_binding (file_id TEXT PRIMARY KEY, upload_type TEXT NOT NULL, entity_type TEXT, entity_id TEXT NOT NULL, created_at DATETIME)`).Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE file (
			id TEXT PRIMARY KEY,
			file_name TEXT,
			extension TEXT,
			mime_type TEXT,
			file_size INTEGER,
			uploaded_by_member_id TEXT,
			delete_requested_at DATETIME
		)
	`).Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE content_document (id TEXT PRIMARY KEY);
		CREATE TABLE content_block (id TEXT PRIMARY KEY, document_id TEXT NOT NULL);
		CREATE TABLE content_block_attachment (
			block_id TEXT NOT NULL,
			reference_path TEXT NOT NULL,
			selector_kind TEXT NOT NULL,
			file_id TEXT,
			PRIMARY KEY (block_id, reference_path)
		);
		CREATE TABLE post (id TEXT PRIMARY KEY, content_document_id TEXT, featured_image_file_id TEXT, status TEXT NOT NULL DEFAULT 'draft');
		CREATE TABLE page (id TEXT PRIMARY KEY, content_document_id TEXT, featured_image_file_id TEXT);
		CREATE TABLE work (id TEXT PRIMARY KEY, content_document_id TEXT, featured_image_file_id TEXT);
		CREATE TABLE program_event (
			id TEXT PRIMARY KEY,
			content_document_id TEXT,
			status TEXT NOT NULL DEFAULT 'draft'
		);
	`).Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE artist (id TEXT PRIMARY KEY);
		CREATE TABLE artist_file (artist_id TEXT NOT NULL, file_id TEXT NOT NULL, PRIMARY KEY (artist_id, file_id));
		CREATE TABLE release (id TEXT PRIMARY KEY);
		CREATE TABLE release_file (release_id TEXT NOT NULL, file_id TEXT NOT NULL, PRIMARY KEY (release_id, file_id));
		CREATE TABLE track (id TEXT PRIMARY KEY, release_id TEXT NOT NULL, audio_original_file_id TEXT);
		CREATE TABLE program_event_media (id TEXT PRIMARY KEY, event_id TEXT NOT NULL, file_id TEXT NOT NULL, role TEXT NOT NULL);
		CREATE TABLE label (id TEXT PRIMARY KEY, logo_light_file_id TEXT, logo_dark_file_id TEXT);
		CREATE TABLE series (id TEXT PRIMARY KEY, featured_image_file_id TEXT);
		CREATE TABLE form (id TEXT PRIMARY KEY, featured_image_file_id TEXT);
	`).Error)
	return db
}

func seedFileDeliveryBinding(t *testing.T, db *gorm.DB, fileID string, uploadType managev1.UploadType, entityType managev1.TranscodeEntityType, entityID string, uploadedBy ...string) {
	t.Helper()
	var entityTypeName *string
	if entityType != managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED {
		value := entityType.String()
		entityTypeName = &value
	}
	uploaderID := ""
	if len(uploadedBy) > 0 {
		uploaderID = uploadedBy[0]
	} else if uploadType == managev1.UploadType_UPLOAD_TYPE_USER_AVATAR {
		uploaderID = entityID
	}
	var uploader any
	if uploaderID != "" {
		uploader = uploaderID
	}
	require.NoError(t, db.Exec(`INSERT INTO file (id, uploaded_by_member_id) VALUES (?, ?)`, fileID, uploader).Error)
	require.NoError(t, db.Create(&model.FileIngestBinding{FileID: fileID, UploadType: uploadType.String(), EntityType: entityTypeName, EntityID: entityID}).Error)
}

func seedCurrentPostFileDelivery(t *testing.T, db *gorm.DB, fileIDs []string) string {
	return seedCurrentContentFileDelivery(t, db, "post", fileIDs, false)
}

func seedCurrentContentFileDelivery(t *testing.T, db *gorm.DB, table string, fileIDs []string, featured bool) string {
	t.Helper()
	entityID := uuid.NewString()
	documentID := uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO content_document (id) VALUES (?)`, documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO `+table+` (id, content_document_id) VALUES (?, ?)`, entityID, documentID).Error)
	for index, fileID := range fileIDs {
		require.NoError(t, db.Exec(`
			INSERT INTO file (id, file_name, extension, mime_type, file_size)
			VALUES (?, ?, 'mp4', 'video/mp4', 4096)
		`, fileID, fmt.Sprintf("%s-attachment-%d.mp4", table, index)).Error)
	}
	if featured {
		require.Len(t, fileIDs, 1)
		require.NoError(t, db.Exec(`UPDATE `+table+` SET featured_image_file_id = ? WHERE id = ?`, fileIDs[0], entityID).Error)
		return entityID
	}
	for index, fileID := range fileIDs {
		blockID := uuid.NewString()
		require.NoError(t, db.Exec(`INSERT INTO content_block (id, document_id) VALUES (?, ?)`, blockID, documentID).Error)
		require.NoError(t, db.Exec(
			`INSERT INTO content_block_attachment (block_id, reference_path, selector_kind, file_id) VALUES (?, ?, 'active', ?)`,
			blockID,
			fmt.Sprintf("attachment-%d", index),
			fileID,
		).Error)
	}
	return entityID
}

func seedFileDeliveryContentPolicy(t *testing.T, spiceDB *auth.SpiceDBClient, resourceType string, resourceID string) {
	t.Helper()
	var policy policyv1.RelationshipMutation
	var err error
	switch resourceType {
	case "post":
		policy, err = policyv1.Post.TouchPolicy(resourceID)
	case "page":
		policy, err = policyv1.Page.TouchPolicy(resourceID)
	case "work":
		policy, err = policyv1.Work.TouchPolicy(resourceID)
	case "program_event":
		policy, err = policyv1.ProgramEvent.TouchPolicy(resourceID)
	default:
		require.FailNow(t, "unsupported FileDelivery content policy resource", resourceType)
		return
	}
	require.NoError(t, err)
	_, err = spiceDB.ApplyRelationships(t.Context(), policy)
	require.NoError(t, err)
}

func seedCurrentSchemaPostFileDelivery(t *testing.T, db *gorm.DB) (string, string) {
	t.Helper()
	postID, documentID, blockID, fileID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile) VALUES (?, 'post')`, documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO post (id, content_document_id) VALUES (?, ?)`, postID, documentID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO file (id, file_name, extension, mime_type, file_size)
		VALUES (?, 'post-attachment', 'mp4', 'video/mp4', 4096)
	`, fileID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO content_block (id, document_id, container_slot, position, kind)
		VALUES (?, ?, 'content', 0, 'file')
	`, blockID, documentID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO content_block_attachment (block_id, reference_path, selector_kind, file_id)
		VALUES (?, 'primary', 'active', ?)
	`, blockID, fileID).Error)
	return postID, fileID
}

func fileDeliveryPrincipalContext(user *testutil.OryUser) context.Context {
	return auth.WithUser(context.Background(), user.AuthUserInfo())
}
