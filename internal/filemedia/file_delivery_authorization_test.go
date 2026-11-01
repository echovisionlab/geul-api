package filemedia

import (
	"context"
	"fmt"
	"testing"

	errs "github.com/echovisionlab/geul-api/internal/errors"

	"connectrpc.com/connect"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestAuthorizeManageFileDeliveriesRequiresIndependentFileUploader(t *testing.T) {
	t.Parallel()
	db := newFileDeliveryAuthorizationDB(t)
	checker := &authztest.Checker{}
	spiceDB := checker.Client(t)
	manager := newUnitUser()
	other := newUnitUser()
	admin := newUnitUser()
	checker.For(admin.IdentityID).Allow(policyv1.File.List())
	svc := withNativeFileUnits(&Service{spiceDB: spiceDB}, db, nativeFilePorts{Posts: newIntegrationPostAccess(db, spiceDB), Events: newIntegrationProgramEventAccess(db)})
	for _, uploadType := range []managev1.UploadType{
		managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO,
	} {
		fileID := uuid.NewString()
		seedFileDeliveryBinding(t, db, fileID, uploadType, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED, "", manager.MemberID)
		require.NoError(t, svc.authorizeManageFileDeliveries(fileDeliveryPrincipalContext(manager), []string{fileID}))
		require.Equal(t, connect.CodePermissionDenied, errs.CodeOf(svc.authorizeManageFileDeliveries(fileDeliveryPrincipalContext(other), []string{fileID})))
		require.NoError(t, svc.authorizeManageFileDeliveries(fileDeliveryPrincipalContext(admin), []string{fileID}))
	}
}

func TestAuthorizeManageFileDeliveriesFailsClosedForMissingOrMixedBindings(t *testing.T) {
	t.Parallel()
	db := newFileDeliveryAuthorizationDB(t)
	checker := &authztest.Checker{}
	spiceDB := checker.Client(t)
	owner := newUnitUser()
	ownedFileID, missingFileID := uuid.NewString(), uuid.NewString()
	seedFileDeliveryBinding(t, db, ownedFileID, managev1.UploadType_UPLOAD_TYPE_USER_AVATAR, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED, owner.MemberID)
	svc := withNativeFileUnits(&Service{spiceDB: spiceDB}, db, nativeFilePorts{Posts: newIntegrationPostAccess(db, spiceDB), Events: newIntegrationProgramEventAccess(db)})
	ctx := fileDeliveryPrincipalContext(owner)
	require.Equal(t, connect.CodePermissionDenied, errs.CodeOf(svc.authorizeManageFileDeliveries(ctx, []string{missingFileID})))
	require.Equal(t, connect.CodePermissionDenied, errs.CodeOf(svc.authorizeManageFileDeliveries(ctx, []string{ownedFileID, missingFileID})))
}

func TestAuthorizeManageFileDeliveriesSupportsOwnerAndAdminBoundaries(t *testing.T) {
	t.Parallel()
	db := newFileDeliveryAuthorizationDB(t)
	checker := &authztest.Checker{}
	spiceDB := checker.Client(t)
	avatarOwner := newUnitUser()
	otherUser := newUnitUser()
	ordinaryUser := newUnitUser()
	admin := newUnitUser()
	checker.For(admin.IdentityID).Allow(policyv1.File.List())
	avatarFileID, mapFileID, mapEntityID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	seedFileDeliveryBinding(t, db, avatarFileID, managev1.UploadType_UPLOAD_TYPE_USER_AVATAR, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED, avatarOwner.MemberID)
	require.NoError(t, db.Exec(`CREATE TABLE map_place (id TEXT PRIMARY KEY)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO map_place (id) VALUES (?)`, mapEntityID).Error)
	seedFileDeliveryBinding(t, db, mapFileID, managev1.UploadType_UPLOAD_TYPE_MAP_IMAGE, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED, mapEntityID)
	svc := withNativeFileUnits(&Service{spiceDB: spiceDB}, db, nativeFilePorts{Posts: newIntegrationPostAccess(db, spiceDB), Events: newIntegrationProgramEventAccess(db)})
	require.NoError(t, svc.authorizeManageFileDeliveries(fileDeliveryPrincipalContext(avatarOwner), []string{avatarFileID}))
	require.Equal(t, connect.CodePermissionDenied, errs.CodeOf(svc.authorizeManageFileDeliveries(fileDeliveryPrincipalContext(otherUser), []string{avatarFileID})))
	require.Equal(t, connect.CodePermissionDenied, errs.CodeOf(svc.authorizeManageFileDeliveries(fileDeliveryPrincipalContext(ordinaryUser), []string{mapFileID})))
	require.NoError(t, svc.authorizeManageFileDeliveries(fileDeliveryPrincipalContext(admin), []string{avatarFileID, mapFileID}))
}

func TestAuthorizeManageFileDeliveriesUsesAvatarTargetOwnerNotUploader(t *testing.T) {
	t.Parallel()
	db := newFileDeliveryAuthorizationDB(t)
	checker := &authztest.Checker{}
	spiceDB := checker.Client(t)
	avatarOwner := newUnitUser()
	adminUploader := newUnitUser()
	fileID := uuid.NewString()
	seedFileDeliveryBinding(
		t, db, fileID, managev1.UploadType_UPLOAD_TYPE_USER_AVATAR,
		managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED,
		avatarOwner.MemberID, adminUploader.MemberID,
	)
	svc := withNativeFileUnits(&Service{spiceDB: spiceDB}, db, nativeFilePorts{Posts: newIntegrationPostAccess(db, spiceDB), Events: newIntegrationProgramEventAccess(db)})
	authorization, err := svc.authorizeManageFileDeliveriesWithWitness(fileDeliveryPrincipalContext(avatarOwner), []string{fileID})
	require.NoError(t, err)
	require.Equal(t, avatarOwner.MemberID, authorization.files[fileID].expectedAvatarOwner)
	require.Empty(t, authorization.files[fileID].expectedUploader)
}

func TestManageMediaDeliveryRejectsArbitraryAndMixedFileIDsBeforeSigning(t *testing.T) {
	t.Parallel()
	db := newFileDeliveryAuthorizationDB(t)
	checker := &authztest.Checker{}
	spiceDB := checker.Client(t)
	owner := newUnitUser()
	ownedFileID, arbitraryFileID := uuid.NewString(), uuid.NewString()
	seedFileDeliveryBinding(t, db, ownedFileID, managev1.UploadType_UPLOAD_TYPE_USER_AVATAR, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED, owner.MemberID)
	svc := withNativeFileUnits(&Service{spiceDB: spiceDB}, db, nativeFilePorts{Posts: newIntegrationPostAccess(db, spiceDB), Events: newIntegrationProgramEventAccess(db)})
	ctx := fileDeliveryPrincipalContext(owner)
	_, err := NewConnectHandler(svc).GetMediaDelivery(ctx, connect.NewRequest(&managev1.GetMediaDeliveryRequest{FileId: arbitraryFileID}))
	require.Equal(t, connect.CodePermissionDenied, errs.CodeOf(err))
	_, err = NewConnectHandler(svc).GetBulkMediaDeliveries(ctx, connect.NewRequest(&managev1.GetBulkMediaDeliveriesRequest{FileIds: []string{ownedFileID, arbitraryFileID}}))
	require.Equal(t, connect.CodePermissionDenied, errs.CodeOf(err))
}

func TestAuthorizeManageFileDeliveriesRejectsUnauthenticatedAndBannedUsers(t *testing.T) {
	t.Parallel()
	db := newFileDeliveryAuthorizationDB(t)
	checker := &authztest.Checker{}
	spiceDB := checker.Client(t)
	owner := newUnitUser()
	fileID := uuid.NewString()
	seedFileDeliveryBinding(t, db, fileID, managev1.UploadType_UPLOAD_TYPE_USER_AVATAR, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED, owner.MemberID)
	svc := withNativeFileUnits(&Service{spiceDB: spiceDB}, db, nativeFilePorts{Posts: newIntegrationPostAccess(db, spiceDB), Events: newIntegrationProgramEventAccess(db)})
	require.Equal(t, connect.CodeUnauthenticated, errs.CodeOf(svc.authorizeManageFileDeliveries(context.Background(), []string{fileID})))
	banned := auditedUserContext(context.Background(), &auth.UserInfo{IdentityID: auth.IdentityID(owner.IdentityID), MemberID: auth.MemberID(owner.MemberID), Authenticated: true, Banned: true})
	require.Equal(t, connect.CodePermissionDenied, errs.CodeOf(svc.authorizeManageFileDeliveries(banned, []string{fileID})))
}

func TestAuthorizeManageFileDeliveriesDeduplicatesEntityPermissionChecks(t *testing.T) {
	t.Parallel()
	db := newFileDeliveryAuthorizationDB(t)
	checker := &authztest.Checker{}
	spiceDB := checker.Client(t)
	manager := newUnitUser()
	fileIDs := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
	postID := seedCurrentPostFileDelivery(t, db, fileIDs)
	checker.For(manager.IdentityID).Allow(policyv1.Post.View(postID))
	svc := withNativeFileUnits(&Service{spiceDB: spiceDB}, db, nativeFilePorts{Posts: newIntegrationPostAccess(db, spiceDB), Events: newIntegrationProgramEventAccess(db)})
	require.NoError(t, svc.authorizeManageFileDeliveries(fileDeliveryPrincipalContext(manager), fileIDs))
	list, err := policyv1.File.List()
	require.NoError(t, err)
	view, err := policyv1.Post.View(postID)
	require.NoError(t, err)
	require.Equal(t, []authztest.Check{
		authztest.Of(manager.IdentityID, list, nil),
		authztest.Of(manager.IdentityID, view, nil),
	}, checker.Checks(), "three Files of one Post check the Post once")
}

func TestAuthorizeManageFileDeliveriesUsesCurrentPostAttachmentsNotIngestBinding(t *testing.T) {
	t.Parallel()
	db := newFileDeliveryAuthorizationDB(t)
	checker := &authztest.Checker{}
	spiceDB := checker.Client(t)
	manager := newUnitUser()
	otherAuthor := newUnitUser()
	fileIDs := []string{uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()}
	postID := seedCurrentPostFileDelivery(t, db, fileIDs)
	checker.For(manager.IdentityID).Allow(policyv1.Post.View(postID))

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

	svc := withNativeFileUnits(&Service{spiceDB: spiceDB}, db, nativeFilePorts{Posts: newIntegrationPostAccess(db, spiceDB), Events: newIntegrationProgramEventAccess(db)})
	managerCtx := fileDeliveryPrincipalContext(manager)
	require.NoError(t, svc.authorizeManageFileDeliveries(managerCtx, fileIDs))
	require.Equal(t, connect.CodeNotFound, errs.CodeOf(
		svc.authorizeManageFileDeliveries(managerCtx, []string{unrelatedFileID}),
	))
	require.Equal(t, connect.CodePermissionDenied, errs.CodeOf(
		svc.authorizeManageFileDeliveries(managerCtx, []string{detachedFileID}),
	))
	require.Equal(t, connect.CodeNotFound, errs.CodeOf(
		svc.authorizeManageFileDeliveries(fileDeliveryPrincipalContext(otherAuthor), []string{fileIDs[0]}),
	))
}

func TestArchivedPostFileBoundariesAllowAuthorViewAndRequireAdminEdit(t *testing.T) {
	t.Parallel()
	db := newFileDeliveryAuthorizationDB(t)
	checker := &authztest.Checker{}
	spiceDB := checker.Client(t)
	author := newUnitUser()
	admin := newUnitUser()
	fileID := uuid.NewString()
	postID := seedCurrentPostFileDelivery(t, db, []string{fileID})
	require.NoError(t, db.Table("post").Where("id = ?", postID).
		Update("status", managev1.PostStatus_POST_STATUS_ARCHIVED.String()).Error)
	// An author of the archived Post may view it; only an administrator
	// edits it.
	checker.For(author.IdentityID).Allow(policyv1.File.List())
	checker.For(author.IdentityID).Allow(policyv1.Post.ViewArchived(postID))
	checker.For(admin.IdentityID).Allow(policyv1.File.List())
	checker.For(admin.IdentityID).Allow(policyv1.Post.EditArchived(postID))

	service := withNativeFileUnits(&Service{spiceDB: spiceDB}, db, nativeFilePorts{Posts: newIntegrationPostAccess(db, spiceDB)})
	authorCtx := fileDeliveryPrincipalContext(author)
	adminCtx := fileDeliveryPrincipalContext(admin)

	require.NoError(t, service.authorizeManageFileDeliveries(authorCtx, []string{fileID}))
	require.NoError(t, service.authorizeFileDownloadPolicyOwner(
		authorCtx,
		testFileStore(service, db),
		fileDownloadPolicyRelation{ResourceType: "post", ResourceID: postID},
		filePolicyOwnerView,
	))
	require.Equal(t, connect.CodeNotFound, errs.CodeOf(
		service.authorizeFileDownloadPolicyOwner(
			authorCtx,
			testFileStore(service, db),
			fileDownloadPolicyRelation{ResourceType: "post", ResourceID: postID},
			filePolicyOwnerEdit,
		),
	))
	require.NoError(t, service.authorizeFileDownloadPolicyOwner(
		adminCtx,
		testFileStore(service, db),
		fileDownloadPolicyRelation{ResourceType: "post", ResourceID: postID},
		filePolicyOwnerEdit,
	))
	require.Equal(t, connect.CodeNotFound, errs.CodeOf(service.checkEntityPermission(
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

	require.Equal(t, connect.CodeNotFound, errs.CodeOf(freshFileIngestAuthorityForTest(
		authorCtx,
		db,
		service,
		managev1.UploadType_UPLOAD_TYPE_FEATURED_IMAGE,
		managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED,
		postID,
	)))
	require.NoError(t, freshFileIngestAuthorityForTest(
		adminCtx,
		db,
		service,
		managev1.UploadType_UPLOAD_TYPE_FEATURED_IMAGE,
		managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED,
		postID,
	))
}

func TestArchivedProgramEventFileBoundariesAllowAuthorViewAndRequireAdminEdit(t *testing.T) {
	t.Parallel()
	db := newFileDeliveryAuthorizationDB(t)
	checker := &authztest.Checker{}
	spiceDB := checker.Client(t)
	author := newUnitUser()
	admin := newUnitUser()
	fileID := uuid.NewString()
	eventID := seedCurrentContentFileDelivery(t, db, "program_event", []string{fileID}, false)
	require.NoError(t, db.Table("program_event").Where("id = ?", eventID).
		Update("status", managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_ARCHIVED.String()).Error)

	checker.For(author.IdentityID).Allow(policyv1.File.List())
	checker.For(author.IdentityID).Allow(policyv1.ProgramEvent.ViewArchived(eventID))
	checker.For(admin.IdentityID).Allow(policyv1.File.List())
	checker.For(admin.IdentityID).Allow(policyv1.ProgramEvent.EditArchived(eventID))

	service := withNativeFileUnits(&Service{spiceDB: spiceDB}, db, nativeFilePorts{Events: newIntegrationProgramEventAccess(db)})
	authorCtx := fileDeliveryPrincipalContext(author)
	adminCtx := fileDeliveryPrincipalContext(admin)

	require.NoError(t, service.authorizeManageFileDeliveries(authorCtx, []string{fileID}))
	require.NoError(t, service.authorizeFileDownloadPolicyOwner(
		authorCtx,
		testFileStore(service, db),
		fileDownloadPolicyRelation{ResourceType: "program_event", ResourceID: eventID},
		filePolicyOwnerView,
	))
	require.Equal(t, connect.CodeNotFound, errs.CodeOf(
		service.authorizeFileDownloadPolicyOwner(
			authorCtx,
			testFileStore(service, db),
			fileDownloadPolicyRelation{ResourceType: "program_event", ResourceID: eventID},
			filePolicyOwnerEdit,
		),
	))
	require.NoError(t, service.authorizeFileDownloadPolicyOwner(
		adminCtx,
		testFileStore(service, db),
		fileDownloadPolicyRelation{ResourceType: "program_event", ResourceID: eventID},
		filePolicyOwnerEdit,
	))
}

func TestAuthorizeManageFileDeliveriesUsesCurrentContentAttachmentsNotIngestBinding(t *testing.T) {
	t.Parallel()
	db := newFileDeliveryAuthorizationDB(t)
	checker := &authztest.Checker{}
	spiceDB := checker.Client(t)
	admin := newUnitUser()
	other := newUnitUser()
	checker.For(admin.IdentityID).Allow(policyv1.File.List())
	svc := withNativeFileUnits(&Service{spiceDB: spiceDB}, db, nativeFilePorts{Posts: newIntegrationPostAccess(db, spiceDB), Events: newIntegrationProgramEventAccess(db)})

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
			require.Equal(t, connect.CodePermissionDenied, errs.CodeOf(
				svc.authorizeManageFileDeliveries(fileDeliveryPrincipalContext(other), []string{detachedFileID}),
			))
		})
	}
}

func TestAuthorizeManageFileDeliveriesGlobalAuthorIsIndependentOfCurrentContentOwners(t *testing.T) {
	t.Parallel()
	db := newFileDeliveryAuthorizationDB(t)
	checker := &authztest.Checker{}
	spiceDB := checker.Client(t)
	manager := newUnitUser()
	fileID := uuid.NewString()
	seedCurrentPostFileDelivery(t, db, []string{fileID})
	checker.For(manager.IdentityID).Allow(policyv1.File.List())

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

	svc := withNativeFileUnits(&Service{spiceDB: spiceDB}, db, nativeFilePorts{Posts: newIntegrationPostAccess(db, spiceDB), Events: newIntegrationProgramEventAccess(db)})
	require.NoError(t, svc.authorizeManageFileDeliveries(fileDeliveryPrincipalContext(manager), []string{fileID}))
}

func TestAuthorizeManageFileDeliveriesRejectsPendingDeleteFile(t *testing.T) {
	t.Parallel()
	db := newFileDeliveryAuthorizationDB(t)
	checker := &authztest.Checker{}
	spiceDB := checker.Client(t)
	uploader := newUnitUser()
	admin := newUnitUser()
	fileID := uuid.NewString()
	seedFileDeliveryBinding(t, db, fileID, managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED, "", uploader.MemberID)
	require.NoError(t, db.Exec(`UPDATE file SET delete_requested_at = CURRENT_TIMESTAMP WHERE id = ?`, fileID).Error)
	svc := withNativeFileUnits(&Service{spiceDB: spiceDB}, db, nativeFilePorts{Posts: newIntegrationPostAccess(db, spiceDB), Events: newIntegrationProgramEventAccess(db)})
	for _, principal := range []*unitUser{uploader, admin} {
		require.Equal(t, connect.CodePermissionDenied, errs.CodeOf(
			svc.authorizeManageFileDeliveries(fileDeliveryPrincipalContext(principal), []string{fileID}),
		))
	}
}

func TestAuthorizeManageFileDeliveriesAllowsGlobalLibraryAuthorForTrackAudio(t *testing.T) {
	t.Parallel()
	db := newFileDeliveryAuthorizationDB(t)
	checker := &authztest.Checker{}
	spiceDB := checker.Client(t)
	author := newUnitUser()
	admin := newUnitUser()
	checker.For(author.IdentityID).Allow(policyv1.File.List())
	checker.For(admin.IdentityID).Allow(policyv1.File.List())
	releaseID := uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO release (id) VALUES (?)`, releaseID).Error)
	fileIDs := []string{uuid.NewString(), uuid.NewString()}
	for _, fileID := range fileIDs {
		trackID := uuid.NewString()
		require.NoError(t, db.Exec(`INSERT INTO track (id, release_id) VALUES (?, ?)`, trackID, releaseID).Error)
		seedFileDeliveryBinding(t, db, fileID, managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED, trackID)
	}
	svc := withNativeFileUnits(&Service{spiceDB: spiceDB}, db, nativeFilePorts{Posts: newIntegrationPostAccess(db, spiceDB), Events: newIntegrationProgramEventAccess(db)})
	require.NoError(t, svc.authorizeManageFileDeliveries(fileDeliveryPrincipalContext(author), fileIDs))
	require.NoError(t, svc.authorizeManageFileDeliveries(fileDeliveryPrincipalContext(admin), fileIDs))
}

func newFileDeliveryAuthorizationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE file_ingest_binding (file_id TEXT PRIMARY KEY, upload_type TEXT NOT NULL, entity_type TEXT, entity_id TEXT NOT NULL, created_at DATETIME)`).Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE file ( client_media_bundle_id TEXT,
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

func fileDeliveryPrincipalContext(user *unitUser) context.Context {
	return auditedUserContext(context.Background(), user.AuthUserInfo())
}
