package workerpostgres

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/worker"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func newUploadSessionDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:expired-upload-"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE upload_session (
		client_media_bundle_id TEXT,
		client_media_manifest TEXT,
		upload_type TEXT NOT NULL,
		upload_id TEXT PRIMARY KEY,
		file_id TEXT NOT NULL,
		requested_mime TEXT NOT NULL,
		entity_id TEXT NOT NULL,
		entity_type TEXT,
		slot_id TEXT,
		attempt_id TEXT,
		expected_current_file_id TEXT,
		status TEXT NOT NULL,
		last_activity_at DATETIME NOT NULL
	)`).Error)
	return db
}

func insertUploadSession(t *testing.T, db *gorm.DB, uploadType managev1.UploadType, uploadID, mime, status string, expectedCurrentFileID any, lastActivity time.Time) string {
	t.Helper()
	fileID := uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO upload_session (
			upload_type, upload_id, file_id, requested_mime, entity_id, attempt_id,
			expected_current_file_id, status, last_activity_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uploadType.String(), uploadID, fileID, mime, "", uuid.NewString(), expectedCurrentFileID, status, lastActivity,
	).Error)
	return fileID
}

func TestExpiredUploadsSelectUnfinishedSessionsPastTheCutoff(t *testing.T) {
	t.Parallel()

	db := newUploadSessionDB(t)
	cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour)
	expiredAt := cutoff.Add(-24 * time.Hour)
	expectedTrack := uuid.NewString()
	audioFile := insertUploadSession(t, db, managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO, "editor-audio", "audio/mpeg", "uploading", nil, expiredAt)
	insertUploadSession(t, db, managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE, "editor-image", "image/jpeg; charset=binary", "initiated", nil, expiredAt)
	insertUploadSession(t, db, managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO, "track", "audio/wav", "failed", expectedTrack, expiredAt)
	insertUploadSession(t, db, managev1.UploadType_UPLOAD_TYPE_USER_AVATAR, "aborted", "application/x-unknown", "aborted", nil, expiredAt)
	insertUploadSession(t, db, managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO, "finalizing", "audio/mpeg", "finalizing", nil, expiredAt)
	insertUploadSession(t, db, managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO, "recent", "audio/mpeg", "uploading", nil, cutoff.Add(time.Hour))

	expired, err := uploadSessions{db: db}.ExpiredUploads(t.Context(), cutoff)
	require.NoError(t, err)

	byUploadID := map[string]worker.ExpiredUpload{}
	for _, session := range expired {
		byUploadID[session.UploadID] = session
	}
	require.ElementsMatch(t, []string{"editor-audio", "editor-image", "track", "aborted"}, slices.Collect(maps.Keys(byUploadID)),
		"a finalizing session is its finalizer's, a recent one is still active")
	require.Equal(t, audioFile, byUploadID["editor-audio"].FileID)
	require.Equal(t, "mp3", byUploadID["editor-audio"].Extension)
	require.Equal(t, "jpg", byUploadID["editor-image"].Extension, "the MIME parameters do not change the extension")
	require.Equal(t, "bin", byUploadID["aborted"].Extension)
	require.Nil(t, byUploadID["editor-audio"].ExpectedCurrentFileID)
	require.Equal(t, expectedTrack, *byUploadID["track"].ExpectedCurrentFileID)
	require.NotNil(t, byUploadID["track"].AttemptID)
}

func TestClaimedUploadIsDeletedOnlyUntilNewActivity(t *testing.T) {
	t.Parallel()

	db := newUploadSessionDB(t)
	cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour)
	insertUploadSession(t, db, managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO, "claimed", "audio/mpeg", "uploading", nil, cutoff.Add(-time.Hour))
	insertUploadSession(t, db, managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO, "finalizing", "audio/mpeg", "finalizing", nil, cutoff.Add(-time.Hour))
	insertUploadSession(t, db, managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO, "touched", "audio/mpeg", "uploading", nil, cutoff.Add(-time.Hour))
	sessions := uploadSessions{db: db}

	claimed, err := sessions.ClaimExpiredUpload(t.Context(), "finalizing", cutoff)
	require.NoError(t, err)
	require.False(t, claimed)
	deleted, err := sessions.DeleteAbortedUpload(t.Context(), "finalizing", cutoff)
	require.NoError(t, err)
	require.Zero(t, deleted, "an unclaimed session is not deleted")

	for _, uploadID := range []string{"claimed", "touched"} {
		claimed, err = sessions.ClaimExpiredUpload(t.Context(), uploadID, cutoff)
		require.NoError(t, err)
		require.True(t, claimed)
	}
	require.NoError(t, db.Exec(`UPDATE upload_session SET last_activity_at = ? WHERE upload_id = 'touched'`, time.Now().UTC()).Error)

	deleted, err = sessions.DeleteAbortedUpload(t.Context(), "claimed", cutoff)
	require.NoError(t, err)
	require.Equal(t, int64(1), deleted)
	deleted, err = sessions.DeleteAbortedUpload(t.Context(), "touched", cutoff)
	require.NoError(t, err)
	require.Zero(t, deleted, "activity after the claim keeps the session")
}

func TestUploadStorageAbortReportsAMissingUploadRecognizably(t *testing.T) {
	t.Parallel()

	var requested []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.Method+" "+r.URL.Path+" "+r.URL.Query().Get("uploadId"))
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`<Error><Code>NoSuchUpload</Code><Message>missing</Message></Error>`))
	}))
	t.Cleanup(server.Close)
	client := s3.New(s3.Options{
		Region: "us-east-1", BaseEndpoint: aws.String(server.URL), UsePathStyle: true,
		Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
	})

	err := uploadStorage{client: client, bucket: "media"}.AbortMultipartUpload(t.Context(), "media/file.jpg", "upload-1")

	require.True(t, filemedia.IsMissingMultipartUploadAbortError(err), "%v", err)
	require.Equal(t, []string{"DELETE /media/media/file.jpg upload-1"}, requested)
}
