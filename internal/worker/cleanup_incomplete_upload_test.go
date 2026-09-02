package worker

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/config"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func randomWorkerTestID(prefix string) string {
	return prefix + "-" + uuid.NewString()
}

type recordingUnitFileIngestPublisher struct {
	events []proto.Message
	err    error
}

func (p *recordingUnitFileIngestPublisher) PublishFileIngest(
	_ context.Context,
	event proto.Message,
) error {
	p.events = append(p.events, event)
	return p.err
}

func TestFindExpiredUploadSessionsLoadsIndependentFileAndTrackIdentity(t *testing.T) {
	t.Parallel()

	db, err := gorm.Open(sqlite.Open("file:expired-upload-"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE upload_session (
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
	expiredAt := time.Now().UTC().Add(-8 * 24 * time.Hour)
	expectedTrack := uuid.NewString()
	rows := []structured.Values{
		{
			managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO.String(), "editor-audio-upload", uuid.NewString(), "audio/mpeg", "",
			nil, nil, uuid.NewString(), nil, expiredAt,
		},
		{
			managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE.String(), "editor-image-upload", uuid.NewString(), "image/jpeg", "",
			nil, nil, uuid.NewString(), nil, expiredAt,
		},
		{
			managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO.String(), "track-upload", uuid.NewString(), "audio/wav", uuid.NewString(),
			nil, nil, uuid.NewString(), expectedTrack, expiredAt,
		},
		{
			managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO.String(), "finalizing-upload", uuid.NewString(), "audio/mpeg", "",
			nil, nil, uuid.NewString(), nil, expiredAt,
		},
	}
	for index, row := range rows {
		status := "uploading"
		if index == len(rows)-1 {
			status = "finalizing"
		}
		require.NoError(t, db.Exec(`INSERT INTO upload_session (
				upload_type, upload_id, file_id, requested_mime, entity_id, entity_type,
				slot_id, attempt_id,
				expected_current_file_id, status, last_activity_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, append(row[:len(row)-1], status, row[len(row)-1])...).Error)
	}

	handlers := &Handlers{db: db}
	sessions, err := handlers.findExpiredUploadSessions(context.Background(), time.Now().UTC().Add(-7*24*time.Hour))
	require.NoError(t, err)
	require.Len(t, sessions, 3)
	byUploadID := make(map[string]expiredUploadSession, len(sessions))
	for _, session := range sessions {
		byUploadID[session.UploadID] = session
	}
	require.NotContains(t, byUploadID, "finalizing-upload")
	require.Nil(t, byUploadID["editor-audio-upload"].ExpectedCurrentFileID)
	require.Nil(t, byUploadID["editor-image-upload"].ExpectedCurrentFileID)
	require.Equal(t, expectedTrack, uploadStringValue(byUploadID["track-upload"].ExpectedCurrentFileID))
}

func TestPublishExpiredFileIngestPublishesIndependentEditorFileIdentity(t *testing.T) {
	t.Parallel()

	handlers := &Handlers{}
	uploadID := randomWorkerTestID("upload")
	fileID := uuid.NewString()
	publisher := &recordingUnitFileIngestPublisher{}
	handlers.fileIngest = publisher

	published, err := handlers.publishExpiredFileIngest(context.Background(), expiredUploadSession{
		UploadType:    managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO.String(),
		UploadID:      uploadID,
		FileID:        fileID,
		RequestedMime: "audio/mpeg",
	}, time.Now().UTC())
	require.NoError(t, err)
	require.True(t, published)
	require.Len(t, publisher.events, 1)
	failedEvent, ok := publisher.events[0].(*managev1.FileIngestFailedEvent)
	require.True(t, ok)
	require.Equal(t, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_FILE, failedEvent.GetIdentity().GetEntityType())
	require.Equal(t, fileID, failedEvent.GetIdentity().GetEntityId())
}

func TestExpiredUploadMediaKind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		uploadType managev1.UploadType
		want       managev1.FileIngestMediaKind
	}{
		{
			name:       "editor image",
			uploadType: managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE,
			want:       managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_EDITOR_IMAGE,
		},
		{
			name:       "editor audio",
			uploadType: managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO,
			want:       managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_EDITOR_AUDIO,
		},
		{
			name:       "editor video",
			uploadType: managev1.UploadType_UPLOAD_TYPE_EDITOR_VIDEO,
			want:       managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_EDITOR_VIDEO,
		},
		{
			name:       "editor attachment",
			uploadType: managev1.UploadType_UPLOAD_TYPE_EDITOR_ATTACHMENT,
			want:       managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_EDITOR_ATTACHMENT,
		},
		{
			name:       "editor mesh",
			uploadType: managev1.UploadType_UPLOAD_TYPE_EDITOR_MESH,
			want:       managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_EDITOR_MESH,
		},
		{
			name:       "track audio",
			uploadType: managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO,
			want:       managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_TRACK_AUDIO,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, expiredUploadMediaKind(tt.uploadType.String()))
		})
	}

	require.Equal(
		t,
		managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_OTHER,
		expiredUploadMediaKind(managev1.UploadType_UPLOAD_TYPE_UNSPECIFIED.String()),
	)
}

func TestPublishExpiredFileIngestPublishesIndependentEditorMeshIdentity(t *testing.T) {
	t.Parallel()

	handlers := &Handlers{}
	uploadID := randomWorkerTestID("upload")
	fileID := uuid.NewString()
	publisher := &recordingUnitFileIngestPublisher{}
	handlers.fileIngest = publisher

	published, err := handlers.publishExpiredFileIngest(context.Background(), expiredUploadSession{
		UploadType:    managev1.UploadType_UPLOAD_TYPE_EDITOR_MESH.String(),
		UploadID:      uploadID,
		FileID:        fileID,
		RequestedMime: "model/gltf-binary",
	}, time.Now().UTC())
	require.NoError(t, err)
	require.True(t, published)
	require.Len(t, publisher.events, 1)
	failedEvent, ok := publisher.events[0].(*managev1.FileIngestFailedEvent)
	require.True(t, ok)
	require.Equal(t, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_FILE, failedEvent.GetIdentity().GetEntityType())
	require.Equal(t, fileID, failedEvent.GetIdentity().GetEntityId())
	require.Equal(t, managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_EDITOR_MESH, failedEvent.GetIdentity().GetMediaKind())
}

func TestExpiredFileIngestFailedEventBuildsIdentityForUploadKinds(t *testing.T) {
	t.Parallel()

	expiredAt := time.Unix(1712345678, 123000000).UTC()

	tests := []struct {
		name          string
		session       expiredUploadSession
		wantEntity    managev1.TranscodeEntityType
		wantMediaKind managev1.FileIngestMediaKind
	}{
		{
			name: "independent editor image",
			session: expiredUploadSession{
				UploadType:    managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE.String(),
				UploadID:      randomWorkerTestID("editor-image"),
				FileID:        uuid.NewString(),
				RequestedMime: "image/jpeg",
				AttemptID:     new(uuid.NewString()),
			},
			wantEntity:    managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_FILE,
			wantMediaKind: managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_EDITOR_IMAGE,
		},
		{
			name: "independent editor audio",
			session: expiredUploadSession{
				UploadType:    managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO.String(),
				UploadID:      randomWorkerTestID("editor-audio"),
				FileID:        uuid.NewString(),
				RequestedMime: "audio/mpeg",
				AttemptID:     new(uuid.NewString()),
			},
			wantEntity:    managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_FILE,
			wantMediaKind: managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_EDITOR_AUDIO,
		},
		{
			name: "independent editor attachment",
			session: expiredUploadSession{
				UploadType:    managev1.UploadType_UPLOAD_TYPE_EDITOR_ATTACHMENT.String(),
				UploadID:      randomWorkerTestID("editor-attachment"),
				FileID:        uuid.NewString(),
				RequestedMime: "application/pdf",
				AttemptID:     new(uuid.NewString()),
			},
			wantEntity:    managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_FILE,
			wantMediaKind: managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_EDITOR_ATTACHMENT,
		},
		{
			name: "independent editor mesh",
			session: expiredUploadSession{
				UploadType:    managev1.UploadType_UPLOAD_TYPE_EDITOR_MESH.String(),
				UploadID:      randomWorkerTestID("editor-mesh"),
				FileID:        uuid.NewString(),
				RequestedMime: "model/gltf-binary",
				AttemptID:     new(uuid.NewString()),
			},
			wantEntity:    managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_FILE,
			wantMediaKind: managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_EDITOR_MESH,
		},
		{
			name: "track audio",
			session: expiredUploadSession{
				UploadType:            managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO.String(),
				UploadID:              randomWorkerTestID("track-audio"),
				FileID:                uuid.NewString(),
				RequestedMime:         "audio/wav",
				EntityID:              uuid.NewString(),
				AttemptID:             new(uuid.NewString()),
				ExpectedCurrentFileID: new(uuid.NewString()),
			},
			wantEntity:    managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK,
			wantMediaKind: managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_TRACK_AUDIO,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			entityType := expiredUploadEntityType(tt.session)
			require.True(t, isExpiredFileIngestLifecyclePublishable(tt.session))

			got, err := expiredFileIngestFailedEvent(tt.session, entityType, expiredAt)
			require.NoError(t, err)

			require.Equal(t, tt.session.UploadID, got.GetCorrelationId())
			require.Equal(t, int64(1), got.GetSequenceNumber())
			require.Equal(t, expiredAt.UnixMilli(), got.GetTimestampMs())
			require.Equal(t, tt.wantEntity, got.GetIdentity().GetEntityType())
			require.Equal(t, expiredUploadEntityID(tt.session, entityType), got.GetIdentity().GetEntityId())
			require.Equal(t, tt.session.FileID, got.GetIdentity().GetFileId())
			require.Equal(t, managev1.FileIngestSource_FILE_INGEST_SOURCE_DIRECT_UPLOAD, got.GetIdentity().GetSource())
			require.Equal(t, tt.wantMediaKind, got.GetIdentity().GetMediaKind())
			require.Equal(t, tt.session.UploadID, got.GetIdentity().GetUploadId())
			require.Equal(t, uploadStringValue(tt.session.SlotID), got.GetIdentity().GetSlotId())
			require.Equal(t, uploadStringValue(tt.session.AttemptID), got.GetIdentity().GetAttemptId())
			require.Equal(t, uploadStringValue(tt.session.ExpectedCurrentFileID), got.GetIdentity().GetExpectedCurrentFileId())
			require.Equal(t, managev1.FileIngestFailureReason_FILE_INGEST_FAILURE_REASON_EXPIRED, got.GetReason())
			require.Equal(t, "upload session expired before file verification", got.GetError())
			require.Equal(t, int32(0), got.GetProgress().GetPercentage())
		})
	}
}

func TestPublishExpiredFileIngestAcceptsIndependentEditorFileIdentity(t *testing.T) {
	t.Parallel()

	publisher := &recordingUnitFileIngestPublisher{}
	handlers := &Handlers{fileIngest: publisher}

	published, err := handlers.publishExpiredFileIngest(context.Background(), expiredUploadSession{
		UploadType:    managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO.String(),
		UploadID:      randomWorkerTestID("upload"),
		FileID:        uuid.NewString(),
		RequestedMime: "audio/mpeg",
	}, time.Now().UTC())
	require.NoError(t, err)
	require.True(t, published)
	require.Len(t, publisher.events, 1)
}

func TestCleanupExpiredUploadSessionAbortsStorageBeforeDeletingSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		session          expiredUploadSession
		wantPublished    bool
		publisherFailure error
	}{
		{
			name: "independent editor image",
			session: expiredUploadSession{
				UploadType:    managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE.String(),
				UploadID:      randomWorkerTestID("editor-image"),
				FileID:        uuid.NewString(),
				RequestedMime: "image/jpeg",
			},
			wantPublished: true,
		},
		{
			name: "independent editor audio",
			session: expiredUploadSession{
				UploadType:    managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO.String(),
				UploadID:      randomWorkerTestID("editor-audio"),
				FileID:        uuid.NewString(),
				RequestedMime: "audio/mpeg",
			},
			wantPublished: true,
		},
		{
			name: "track",
			session: expiredUploadSession{
				UploadType:    managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO.String(),
				UploadID:      randomWorkerTestID("track"),
				FileID:        uuid.NewString(),
				RequestedMime: "audio/wav",
				EntityID:      uuid.NewString(),
			},
			wantPublished: true,
		},
		{
			name: "ordinary upload",
			session: expiredUploadSession{
				UploadType:    managev1.UploadType_UPLOAD_TYPE_USER_AVATAR.String(),
				UploadID:      randomWorkerTestID("ordinary"),
				FileID:        uuid.NewString(),
				RequestedMime: "image/jpeg",
				EntityID:      uuid.NewString(),
			},
		},
		{
			name: "realtime projection failure is best effort",
			session: expiredUploadSession{
				UploadType:    managev1.UploadType_UPLOAD_TYPE_EDITOR_ATTACHMENT.String(),
				UploadID:      randomWorkerTestID("publisher-failure"),
				FileID:        uuid.NewString(),
				RequestedMime: "application/pdf",
			},
			wantPublished:    true,
			publisherFailure: errors.New("publisher unavailable"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db, cutoff := newExpiredUploadCleanupDB(t, tt.session)
			abortRequests := make(chan string, 1)
			s3Client := newFileDeleteTestS3Client(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				abortRequests <- r.URL.Query().Get("uploadId")
				w.WriteHeader(http.StatusNoContent)
			}))
			publisher := &recordingUnitFileIngestPublisher{err: tt.publisherFailure}
			handlers := &Handlers{
				db:         db,
				config:     &config.Config{S3Bucket: "media"},
				s3Client:   s3Client,
				fileIngest: publisher,
			}

			removed, err := handlers.cleanupExpiredUploadSession(context.Background(), tt.session, cutoff, cutoff)
			require.NoError(t, err)
			require.True(t, removed)
			require.Equal(t, tt.session.UploadID, <-abortRequests)
			var count int64
			require.NoError(t, db.Table("upload_session").Where("upload_id = ?", tt.session.UploadID).Count(&count).Error)
			require.Zero(t, count)
			if tt.wantPublished {
				require.Len(t, publisher.events, 1)
			} else {
				require.Empty(t, publisher.events)
			}
		})
	}
}

func TestCleanupExpiredUploadSessionRetainsClaimWhenStorageAbortFails(t *testing.T) {
	t.Parallel()

	session := expiredUploadSession{
		UploadType:    managev1.UploadType_UPLOAD_TYPE_USER_AVATAR.String(),
		UploadID:      randomWorkerTestID("abort-failure"),
		FileID:        uuid.NewString(),
		RequestedMime: "image/jpeg",
		EntityID:      uuid.NewString(),
	}
	db, cutoff := newExpiredUploadCleanupDB(t, session)
	s3Client := newFileDeleteTestS3Client(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
	}))
	handlers := &Handlers{db: db, config: &config.Config{S3Bucket: "media"}, s3Client: s3Client}

	removed, err := handlers.cleanupExpiredUploadSession(context.Background(), session, cutoff, cutoff)
	require.Error(t, err)
	require.False(t, removed)
	var status string
	require.NoError(t, db.Table("upload_session").Select("status").Where("upload_id = ?", session.UploadID).Scan(&status).Error)
	require.Equal(t, "aborted", status)
}

func TestCleanupExpiredUploadSessionTreatsMissingMultipartUploadAsClean(t *testing.T) {
	t.Parallel()

	session := expiredUploadSession{
		UploadType:    managev1.UploadType_UPLOAD_TYPE_USER_AVATAR.String(),
		UploadID:      randomWorkerTestID("already-missing"),
		FileID:        uuid.NewString(),
		RequestedMime: "image/jpeg",
		EntityID:      uuid.NewString(),
	}
	db, cutoff := newExpiredUploadCleanupDB(t, session)
	s3Client := newFileDeleteTestS3Client(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`<Error><Code>NoSuchUpload</Code><Message>missing</Message></Error>`))
	}))
	handlers := &Handlers{db: db, config: &config.Config{S3Bucket: "media"}, s3Client: s3Client}

	removed, err := handlers.cleanupExpiredUploadSession(context.Background(), session, cutoff, cutoff)
	require.NoError(t, err)
	require.True(t, removed)
}

func newExpiredUploadCleanupDB(t *testing.T, session expiredUploadSession) (*gorm.DB, time.Time) {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:expired-upload-cleanup-"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE upload_session (
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
	expiredAt := time.Now().UTC().Add(-8 * 24 * time.Hour)
	require.NoError(t, db.Exec(`INSERT INTO upload_session (
		upload_type, upload_id, file_id, requested_mime, entity_id, entity_type,
		slot_id, attempt_id,
		expected_current_file_id, status, last_activity_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		session.UploadType,
		session.UploadID,
		session.FileID,
		session.RequestedMime,
		session.EntityID,
		session.EntityType,
		session.SlotID,
		session.AttemptID,
		session.ExpectedCurrentFileID,
		"uploading",
		expiredAt,
	).Error)
	return db, time.Now().UTC().Add(-7 * 24 * time.Hour)
}

func TestPublishExpiredFileIngestRequiresPublisher(t *testing.T) {
	t.Parallel()

	handlers := &Handlers{}
	uploadID := randomWorkerTestID("upload")
	fileID := uuid.NewString()
	releaseID := uuid.NewString()

	published, err := handlers.publishExpiredFileIngest(context.Background(), expiredUploadSession{
		UploadType:    managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO.String(),
		UploadID:      uploadID,
		FileID:        fileID,
		RequestedMime: "audio/mpeg",
		EntityID:      releaseID,
	}, time.Now().UTC())
	require.False(t, published)
	require.Error(t, err)
	require.Contains(t, err.Error(), "file ingest lifecycle publisher is not configured")
}
