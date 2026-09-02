package filemedia

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type recordingPostAccess struct {
	requireViewCount       int
	requireEditCount       int
	requireLockedViewCount int
	requireLockedEditCount int
	lockedViewDB           *gorm.DB
	lockedEditDB           *gorm.DB
	err                    error
}

type recordingProgramEventAccess struct {
	requireViewCount       int
	requireEditCount       int
	requireLockedViewCount int
	requireLockedEditCount int
	lockedViewDB           *gorm.DB
	lockedEditDB           *gorm.DB
	err                    error
}

func (a *recordingProgramEventAccess) RequireView(context.Context, *auth.SpiceDBClient, string) error {
	a.requireViewCount++
	return a.err
}

func (a *recordingProgramEventAccess) RequireEdit(context.Context, *auth.SpiceDBClient, string) error {
	a.requireEditCount++
	return a.err
}

func (a *recordingProgramEventAccess) RequireLockedView(
	_ context.Context,
	tx *gorm.DB,
	_ *auth.SpiceDBClient,
	_ string,
) error {
	a.requireLockedViewCount++
	a.lockedViewDB = tx
	return a.err
}

func (a *recordingProgramEventAccess) RequireLockedEdit(
	_ context.Context,
	tx *gorm.DB,
	_ *auth.SpiceDBClient,
	_ string,
) error {
	a.requireLockedEditCount++
	a.lockedEditDB = tx
	return a.err
}

func (a *recordingPostAccess) RequireView(context.Context, string) error {
	a.requireViewCount++
	return a.err
}

func (a *recordingPostAccess) RequireEdit(context.Context, string) error {
	a.requireEditCount++
	return a.err
}

func (a *recordingPostAccess) RequireLockedView(_ context.Context, tx *gorm.DB, _ string) error {
	a.requireLockedViewCount++
	a.lockedViewDB = tx
	return a.err
}

func (a *recordingPostAccess) RequireLockedEdit(_ context.Context, tx *gorm.DB, _ string) error {
	a.requireLockedEditCount++
	a.lockedEditDB = tx
	return a.err
}

func TestPostUploadAuthorizationUsesOneStatusAwareDomainAction(t *testing.T) {
	t.Parallel()

	postID := uuid.NewString()
	principal := postAccessPrincipal()
	ctx := auth.WithUser(context.Background(), principal)

	t.Run("initiation", func(t *testing.T) {
		access := &recordingPostAccess{}
		service := &FileService{postAccess: access}
		err := service.checkEntityPermission(
			ctx,
			managev1.UploadType_UPLOAD_TYPE_FEATURED_IMAGE,
			"",
			postID,
			principal.MemberID.String(),
		)
		require.NoError(t, err)
		require.Equal(t, 1, access.requireEditCount)
		require.Zero(t, access.requireLockedEditCount)
		require.Zero(t, access.requireViewCount)
	})

	t.Run("part", func(t *testing.T) {
		access := &recordingPostAccess{}
		service := &FileService{postAccess: access}
		err := service.checkPartUploadPermission(ctx, principal.MemberID.String(), postUploadSession(postID))
		require.NoError(t, err)
		require.Equal(t, 1, access.requireEditCount)
		require.Zero(t, access.requireLockedEditCount)
		require.Zero(t, access.requireViewCount)
	})
}

func TestVerifiedPostFileMutationPassesOuterTransactionToOneEditAction(t *testing.T) {
	t.Parallel()

	db := newPostAccessUnitDB(t)
	access := &recordingPostAccess{}
	service := &FileService{db: db, postAccess: access}
	ctx := auth.WithUser(context.Background(), postAccessPrincipal())
	postID := uuid.NewString()

	require.NoError(t, requireFreshFileIngestAuthority(
		ctx,
		db,
		service,
		managev1.UploadType_UPLOAD_TYPE_FEATURED_IMAGE,
		managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED,
		postID,
	))
	require.Equal(t, 1, access.requireLockedEditCount)
	require.Same(t, db, access.lockedEditDB)
	require.Zero(t, access.requireEditCount)
	require.Zero(t, access.requireViewCount)
	require.Zero(t, access.requireLockedViewCount)
}

func TestPostManageDeliveryUsesOneViewAction(t *testing.T) {
	t.Parallel()

	access := &recordingPostAccess{}
	service := &FileService{postAccess: access}
	principal := postAccessPrincipal()
	err := service.authorizeManageFilePermissionTarget(
		auth.WithUser(context.Background(), principal),
		principal,
		manageFilePermissionTarget{resourceType: "post", resourceID: uuid.NewString()},
	)
	require.NoError(t, err)
	require.Equal(t, 1, access.requireViewCount)
	require.Zero(t, access.requireEditCount)
	require.Zero(t, access.requireLockedViewCount)
	require.Zero(t, access.requireLockedEditCount)
}

func TestPostFilePolicyActionsUseOneLockedActionInOuterTransaction(t *testing.T) {
	t.Parallel()

	db := newPostAccessUnitDB(t)
	postID, _ := seedUnitContentFileOwner(t, db, "post")
	ctx := auth.WithUser(context.Background(), postAccessPrincipal())

	viewAccess := &recordingPostAccess{}
	viewService := &FileService{postAccess: viewAccess}
	require.NoError(t, viewService.authorizeFileDownloadPolicyOwner(
		ctx,
		db,
		fileDownloadPolicyRelation{ResourceType: "post", ResourceID: postID},
		filePolicyOwnerView,
	))
	require.Equal(t, 1, viewAccess.requireLockedViewCount)
	require.Same(t, db, viewAccess.lockedViewDB)
	require.Zero(t, viewAccess.requireViewCount)
	require.Zero(t, viewAccess.requireEditCount)
	require.Zero(t, viewAccess.requireLockedEditCount)

	editAccess := &recordingPostAccess{}
	editService := &FileService{postAccess: editAccess}
	require.NoError(t, editService.authorizeFileDownloadPolicyOwner(
		ctx,
		db,
		fileDownloadPolicyRelation{ResourceType: "post", ResourceID: postID},
		filePolicyOwnerEdit,
	))
	require.Equal(t, 1, editAccess.requireLockedEditCount)
	require.Same(t, db, editAccess.lockedEditDB)
	require.Zero(t, editAccess.requireViewCount)
	require.Zero(t, editAccess.requireEditCount)
	require.Zero(t, editAccess.requireLockedViewCount)
}

func TestProgramEventFileOperationsUseOneExactDomainAction(t *testing.T) {
	t.Parallel()

	db := newPostAccessUnitDB(t)
	eventID, _ := seedUnitContentFileOwner(t, db, "program_event")
	ctx := auth.WithUser(context.Background(), postAccessPrincipal())

	deliveryAccess := &recordingProgramEventAccess{}
	deliveryService := &FileService{programEventAttachment: deliveryAccess}
	require.NoError(t, deliveryService.authorizeManageFilePermissionTarget(
		ctx,
		auth.GetUser(ctx),
		manageFilePermissionTarget{resourceType: "program_event", resourceID: eventID},
	))
	require.Equal(t, 1, deliveryAccess.requireViewCount)
	require.Zero(t, deliveryAccess.requireEditCount)
	require.Zero(t, deliveryAccess.requireLockedViewCount)
	require.Zero(t, deliveryAccess.requireLockedEditCount)

	viewAccess := &recordingProgramEventAccess{}
	viewService := &FileService{programEventAttachment: viewAccess}
	require.NoError(t, viewService.authorizeFileDownloadPolicyOwner(
		ctx,
		db,
		fileDownloadPolicyRelation{ResourceType: "program_event", ResourceID: eventID},
		filePolicyOwnerView,
	))
	require.Equal(t, 1, viewAccess.requireLockedViewCount)
	require.Same(t, db, viewAccess.lockedViewDB)
	require.Zero(t, viewAccess.requireViewCount)
	require.Zero(t, viewAccess.requireEditCount)
	require.Zero(t, viewAccess.requireLockedEditCount)

	editAccess := &recordingProgramEventAccess{}
	editService := &FileService{programEventAttachment: editAccess}
	require.NoError(t, editService.authorizeFileDownloadPolicyOwner(
		ctx,
		db,
		fileDownloadPolicyRelation{ResourceType: "program_event", ResourceID: eventID},
		filePolicyOwnerEdit,
	))
	require.Equal(t, 1, editAccess.requireLockedEditCount)
	require.Same(t, db, editAccess.lockedEditDB)
	require.Zero(t, editAccess.requireViewCount)
	require.Zero(t, editAccess.requireEditCount)
	require.Zero(t, editAccess.requireLockedViewCount)
}

func newPostAccessUnitDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
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
		CREATE TABLE post (id TEXT PRIMARY KEY, content_document_id TEXT);
		CREATE TABLE program_event (id TEXT PRIMARY KEY, content_document_id TEXT);
	`).Error)
	return db
}

func seedUnitContentFileOwner(t *testing.T, db *gorm.DB, table string) (string, string) {
	t.Helper()
	entityID, documentID, blockID, fileID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec("INSERT INTO content_document (id) VALUES (?)", documentID).Error)
	require.NoError(t, db.Exec("INSERT INTO "+table+" (id, content_document_id) VALUES (?, ?)", entityID, documentID).Error)
	require.NoError(t, db.Exec("INSERT INTO content_block (id, document_id) VALUES (?, ?)", blockID, documentID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO content_block_attachment (block_id, reference_path, selector_kind, file_id)
		VALUES (?, 'primary', 'active', ?)
	`, blockID, fileID).Error)
	return entityID, fileID
}

func postAccessPrincipal() *auth.UserInfo {
	return &auth.UserInfo{
		IdentityID:    auth.IdentityID(uuid.NewString()),
		MemberID:      auth.MemberID(uuid.NewString()),
		Authenticated: true,
		Onboarded:     true,
	}
}

func postUploadSession(postID string) model.UploadSession {
	entityType := managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST.String()
	return model.UploadSession{
		UploadType: managev1.UploadType_UPLOAD_TYPE_FEATURED_IMAGE.String(),
		EntityType: &entityType,
		EntityID:   postID,
	}
}
