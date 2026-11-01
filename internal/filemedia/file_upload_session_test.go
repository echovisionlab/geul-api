package filemedia

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	errs "github.com/echovisionlab/geul-api/internal/errors"

	"connectrpc.com/connect"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestAbortMultipartUploadReturnsErrorWhenSessionDeleteFails(t *testing.T) {
	t.Parallel()

	service, db, ctx, request, session, abortRequests := newAbortMultipartUploadRPCFixture(
		t,
		model.UploadSessionStatusUploading,
	)
	require.NoError(t, db.Exec(`CREATE TRIGGER reject_abort_rpc_session_delete
		BEFORE DELETE ON upload_session
		BEGIN
			SELECT RAISE(ABORT, 'injected abort RPC session delete failure');
		END`).Error)

	response, err := NewConnectHandler(service).AbortMultipartUpload(ctx, request)
	require.Error(t, err)
	require.Nil(t, response)
	require.Equal(t, connect.CodeInternal, errs.CodeOf(err))
	require.Equal(t, session.UploadID, <-abortRequests)
	var count int64
	require.NoError(t, db.Model(&model.UploadSession{}).Where("upload_id = ?", session.UploadID).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestAbortMultipartUploadRejectsFinalizingSessionBeforeStorageMutation(t *testing.T) {
	t.Parallel()

	service, db, ctx, request, session, abortRequests := newAbortMultipartUploadRPCFixture(
		t,
		model.UploadSessionStatusFinalizing,
	)

	response, err := NewConnectHandler(service).AbortMultipartUpload(ctx, request)
	require.Error(t, err)
	require.Nil(t, response)
	require.Equal(t, connect.CodeFailedPrecondition, errs.CodeOf(err))
	select {
	case uploadID := <-abortRequests:
		t.Fatalf("storage abort called for finalizing session %s", uploadID)
	default:
	}
	var status string
	require.NoError(t, db.Table("upload_session").Select("status").Where("upload_id = ?", session.UploadID).Scan(&status).Error)
	require.Equal(t, string(model.UploadSessionStatusFinalizing), status)
}

func TestRejectedMultipartPartCannotAbortFinalizingSession(t *testing.T) {
	t.Parallel()

	service, db, _, _, session, abortRequests := newAbortMultipartUploadRPCFixture(
		t,
		model.UploadSessionStatusFinalizing,
	)
	request := httptest.NewRequest(http.MethodPut, "/upload-part", nil)
	recorder := httptest.NewRecorder()

	require.False(t, service.rejectMultipartUploadPart(recorder, request, session, "MIME type mismatch"))
	require.Equal(t, http.StatusConflict, recorder.Code)
	select {
	case uploadID := <-abortRequests:
		t.Fatalf("storage abort called for finalizing session %s", uploadID)
	default:
	}

	var status string
	require.NoError(t, db.Table("upload_session").Select("status").Where("upload_id = ?", session.UploadID).Scan(&status).Error)
	require.Equal(t, string(model.UploadSessionStatusFinalizing), status)
}

func newAbortMultipartUploadRPCFixture(
	t *testing.T,
	status model.UploadSessionStatus,
) (*Service, *gorm.DB, context.Context, *connect.Request[managev1.AbortMultipartUploadRequest], model.UploadSession, <-chan string) {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:abort-rpc-"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE upload_session ( client_media_bundle_id TEXT, client_media_manifest TEXT,
		upload_id TEXT PRIMARY KEY,
		file_id TEXT NOT NULL,
		upload_type TEXT NOT NULL,
		entity_id TEXT NOT NULL,
		entity_type TEXT,
		file_name TEXT NOT NULL,
		file_size INTEGER NOT NULL,
		file_last_modified INTEGER,
		slot_id TEXT,
		attempt_id TEXT,
		expected_current_file_id TEXT,
		ingest_sequence INTEGER NOT NULL,
		requested_mime TEXT NOT NULL,
		detected_mime TEXT,
		total_parts INTEGER NOT NULL,
		chunk_size INTEGER NOT NULL,
		status TEXT NOT NULL,
		verified_at DATETIME,
		last_activity_at DATETIME NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`).Error)
	admin := newUnitUser()
	userID := admin.MemberID
	session := model.UploadSession{
		UploadID:       "upload-abort-rpc",
		FileID:         uuid.NewString(),
		UploadType:     managev1.UploadType_UPLOAD_TYPE_USER_AVATAR.String(),
		EntityID:       userID,
		FileName:       "avatar.jpg",
		FileSize:       1,
		RequestedMime:  "image/jpeg",
		TotalParts:     1,
		ChunkSize:      1,
		Status:         status,
		LastActivityAt: time.Now().UTC(),
	}
	require.NoError(t, db.Create(&session).Error)
	abortRequests := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		abortRequests <- r.URL.Query().Get("uploadId")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	service := newTestService(Deps{
		DB:             db,
		S3:             newAvatarAssetTestS3Client(server),
		AsyncPublisher: &hardCutAsyncPublisher{},
		S3Bucket:       "media",
		CDNDomain:      "",
		MediaDomain:    "",
		MediaSecret:    "",
		Transcoder:     &recordingFileTranscoderPublisher{},
		SpiceDB:        authztest.AllowAll().Client(t),
	})
	ctx := auditedUserContext(context.Background(), admin.AuthUserInfo())
	request := connect.NewRequest(&managev1.AbortMultipartUploadRequest{UploadId: session.UploadID, FileId: session.FileID})
	return service, db, ctx, request, session, abortRequests
}

func TestRecordUploadedPartCannotOverwriteFinalizingOrAbortedState(t *testing.T) {
	t.Parallel()

	db, err := gorm.Open(sqlite.Open("file:record-upload-part-"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE upload_session ( client_media_bundle_id TEXT, client_media_manifest TEXT,
		upload_id TEXT PRIMARY KEY,
		file_id TEXT NOT NULL,
		status TEXT NOT NULL,
		last_activity_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE upload_part (
		upload_id TEXT NOT NULL,
		part_number INTEGER NOT NULL,
		etag TEXT NOT NULL,
		size INTEGER NOT NULL,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		PRIMARY KEY (upload_id, part_number)
	)`).Error)
	now := time.Now().UTC()
	session := model.UploadSession{UploadID: "part-race", FileID: "file-race", Status: model.UploadSessionStatusFinalizing}
	require.NoError(t, db.Exec(
		"INSERT INTO upload_session (upload_id, file_id, status, last_activity_at, updated_at) VALUES (?, ?, ?, ?, ?)",
		session.UploadID, session.FileID, session.Status, now, now,
	).Error)
	part := model.UploadPart{UploadID: session.UploadID, PartNumber: 1, ETag: "etag", Size: 10, CreatedAt: now, UpdatedAt: now}
	service := withTestStorage(&Service{db: db})

	recorded, err := service.recordUploadedPart(context.Background(), session, part)
	require.NoError(t, err)
	require.False(t, recorded)
	var count int64
	require.NoError(t, db.Model(&model.UploadPart{}).Where("upload_id = ?", session.UploadID).Count(&count).Error)
	require.Zero(t, count)

	require.NoError(t, db.Model(&model.UploadSession{}).Where("upload_id = ?", session.UploadID).
		Update("status", model.UploadSessionStatusAborted).Error)
	recorded, err = service.recordUploadedPart(context.Background(), session, part)
	require.NoError(t, err)
	require.False(t, recorded)
	require.NoError(t, db.Model(&model.UploadPart{}).Where("upload_id = ?", session.UploadID).Count(&count).Error)
	require.Zero(t, count)
}
