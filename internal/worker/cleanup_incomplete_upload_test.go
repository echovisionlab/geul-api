package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aws/smithy-go"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/echovisionlab/geul-api/internal/filemedia"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
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

func TestPublishExpiredFileIngestPublishesIndependentEditorFileIdentity(t *testing.T) {
	t.Parallel()

	handlers := &Handlers{}
	uploadID := randomWorkerTestID("upload")
	fileID := uuid.NewString()
	publisher := &recordingUnitFileIngestPublisher{}
	handlers.fileIngest = publisher

	published, err := handlers.publishExpiredFileIngest(context.Background(), ExpiredUpload{
		UploadType:    managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO.String(),
		UploadID:      uploadID,
		FileID:        fileID,
		RequestedMime: "audio/mpeg",
		Extension:     "mp3",
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

	published, err := handlers.publishExpiredFileIngest(context.Background(), ExpiredUpload{
		UploadType:    managev1.UploadType_UPLOAD_TYPE_EDITOR_MESH.String(),
		UploadID:      uploadID,
		FileID:        fileID,
		RequestedMime: "model/gltf-binary",
		Extension:     "glb",
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
		session       ExpiredUpload
		wantEntity    managev1.TranscodeEntityType
		wantMediaKind managev1.FileIngestMediaKind
	}{
		{
			name: "independent editor image",
			session: ExpiredUpload{
				UploadType:    managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE.String(),
				UploadID:      randomWorkerTestID("editor-image"),
				FileID:        uuid.NewString(),
				RequestedMime: "image/jpeg",
				Extension:     "jpg",
				AttemptID:     new(uuid.NewString()),
			},
			wantEntity:    managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_FILE,
			wantMediaKind: managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_EDITOR_IMAGE,
		},
		{
			name: "independent editor audio",
			session: ExpiredUpload{
				UploadType:    managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO.String(),
				UploadID:      randomWorkerTestID("editor-audio"),
				FileID:        uuid.NewString(),
				RequestedMime: "audio/mpeg",
				Extension:     "mp3",
				AttemptID:     new(uuid.NewString()),
			},
			wantEntity:    managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_FILE,
			wantMediaKind: managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_EDITOR_AUDIO,
		},
		{
			name: "independent editor attachment",
			session: ExpiredUpload{
				UploadType:    managev1.UploadType_UPLOAD_TYPE_EDITOR_ATTACHMENT.String(),
				UploadID:      randomWorkerTestID("editor-attachment"),
				FileID:        uuid.NewString(),
				RequestedMime: "application/pdf",
				Extension:     "pdf",
				AttemptID:     new(uuid.NewString()),
			},
			wantEntity:    managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_FILE,
			wantMediaKind: managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_EDITOR_ATTACHMENT,
		},
		{
			name: "independent editor mesh",
			session: ExpiredUpload{
				UploadType:    managev1.UploadType_UPLOAD_TYPE_EDITOR_MESH.String(),
				UploadID:      randomWorkerTestID("editor-mesh"),
				FileID:        uuid.NewString(),
				RequestedMime: "model/gltf-binary",
				Extension:     "glb",
				AttemptID:     new(uuid.NewString()),
			},
			wantEntity:    managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_FILE,
			wantMediaKind: managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_EDITOR_MESH,
		},
		{
			name: "track audio",
			session: ExpiredUpload{
				UploadType:            managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO.String(),
				UploadID:              randomWorkerTestID("track-audio"),
				FileID:                uuid.NewString(),
				RequestedMime:         "audio/wav",
				Extension:             "wav",
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
			require.NoError(t, expiredFileIngestLifecycleIdentityError(tt.session))

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

	published, err := handlers.publishExpiredFileIngest(context.Background(), ExpiredUpload{
		UploadType:    managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO.String(),
		UploadID:      randomWorkerTestID("upload"),
		FileID:        uuid.NewString(),
		RequestedMime: "audio/mpeg",
		Extension:     "mp3",
	}, time.Now().UTC())
	require.NoError(t, err)
	require.True(t, published)
	require.Len(t, publisher.events, 1)
}

// uploads plays the upload sessions and their object storage: claim marks
// a session aborted, delete removes an aborted one, and calls records the
// order of the steps.
type uploads struct {
	unreachable
	calls    *calls
	expired  []ExpiredUpload
	cutoffs  []time.Time
	aborted  map[string]bool
	sessions map[string]bool
	claimErr error
	abortErr error
	stolen   map[string]bool // another cleanup claimed it first
	changed  map[string]bool // new activity after the claim
}

func newUploads(t *testing.T, sessions ...ExpiredUpload) *uploads {
	u := &uploads{
		unreachable: unreachable{t: t}, calls: &calls{}, expired: sessions,
		aborted: map[string]bool{}, sessions: map[string]bool{}, stolen: map[string]bool{}, changed: map[string]bool{},
	}
	for _, session := range sessions {
		u.sessions[session.UploadID] = true
	}
	return u
}

func (u *uploads) ExpiredUploads(_ context.Context, cutoff time.Time) ([]ExpiredUpload, error) {
	u.cutoffs = append(u.cutoffs, cutoff)
	return u.expired, nil
}

func (u *uploads) ClaimExpiredUpload(_ context.Context, uploadID string, cutoff time.Time) (bool, error) {
	u.calls.add("claim " + uploadID)
	u.cutoffs = append(u.cutoffs, cutoff)
	if u.claimErr != nil || u.stolen[uploadID] {
		return false, u.claimErr
	}
	u.aborted[uploadID] = true
	return true, nil
}

func (u *uploads) AbortMultipartUpload(_ context.Context, objectKey, uploadID string) error {
	u.calls.add("abort " + objectKey + " " + uploadID)
	return u.abortErr
}

func (u *uploads) CleanupClientMediaStaging(_ context.Context, bundleID, manifest string) error {
	u.calls.add("staging " + bundleID + " " + manifest)
	return nil
}

func (u *uploads) DeleteAbortedUpload(_ context.Context, uploadID string, cutoff time.Time) (int64, error) {
	u.calls.add("delete " + uploadID)
	u.cutoffs = append(u.cutoffs, cutoff)
	if !u.aborted[uploadID] || u.changed[uploadID] {
		return 0, nil
	}
	delete(u.sessions, uploadID)
	return 1, nil
}

func expiredUploadOf(uploadType managev1.UploadType) ExpiredUpload {
	return ExpiredUpload{
		UploadType: uploadType.String(), UploadID: randomWorkerTestID("upload"), FileID: uuid.NewString(),
		RequestedMime: "image/jpeg", Extension: "jpg", EntityID: uuid.NewString(),
	}
}

func TestCleanupExpiredUploadSessionAbortsStorageBeforeDeletingSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		session          ExpiredUpload
		wantPublished    bool
		publisherFailure error
	}{
		{name: "independent editor image", session: expiredUploadOf(managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE), wantPublished: true},
		{name: "independent editor audio", session: expiredUploadOf(managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO), wantPublished: true},
		{name: "track", session: expiredUploadOf(managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO), wantPublished: true},
		{name: "ordinary upload", session: expiredUploadOf(managev1.UploadType_UPLOAD_TYPE_USER_AVATAR)},
		{
			name:             "realtime projection failure is best effort",
			session:          expiredUploadOf(managev1.UploadType_UPLOAD_TYPE_EDITOR_ATTACHMENT),
			wantPublished:    true,
			publisherFailure: errors.New("publisher unavailable"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if tt.session.UploadType != managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO.String() &&
				tt.session.UploadType != managev1.UploadType_UPLOAD_TYPE_USER_AVATAR.String() {
				tt.session.EntityID = ""
			}
			ports := newUploads(t, tt.session)
			publisher := &recordingUnitFileIngestPublisher{err: tt.publisherFailure}
			handlers := newTestHandlers(t, Deps{Uploads: ports, UploadStorage: ports, FileIngest: publisher})
			cutoff := time.Now().Add(-time.Hour)

			removed, err := handlers.cleanupExpiredUploadSession(t.Context(), tt.session, cutoff, cutoff)

			require.NoError(t, err)
			require.True(t, removed)
			require.Equal(t, []string{
				"claim " + tt.session.UploadID,
				"abort media/" + tt.session.FileID + ".jpg " + tt.session.UploadID,
				"delete " + tt.session.UploadID,
			}, ports.calls.all())
			require.Empty(t, ports.sessions)
			require.Equal(t, []time.Time{cutoff, cutoff}, ports.cutoffs, "the claim and the delete recheck the same cutoff")
			if tt.wantPublished {
				require.Len(t, publisher.events, 1)
			} else {
				require.Empty(t, publisher.events)
			}
		})
	}
}

func TestCleanupExpiredUploadSessionDeletesTheClientMediaStaging(t *testing.T) {
	session := expiredUploadOf(managev1.UploadType_UPLOAD_TYPE_USER_AVATAR)
	bundleID, manifest := "bundle-1", `{"files":[]}`
	session.ClientMediaBundleID, session.ClientMediaManifest = &bundleID, &manifest
	ports := newUploads(t, session)
	handlers := newTestHandlers(t, Deps{Uploads: ports, UploadStorage: ports})

	removed, err := handlers.cleanupExpiredUploadSession(t.Context(), session, time.Now(), time.Now())

	require.NoError(t, err)
	require.True(t, removed)
	require.Equal(t, "staging "+bundleID+" "+manifest, ports.calls.all()[2], "after the multipart abort, before the delete")
}

func TestCleanupExpiredUploadSessionRetainsClaimWhenStorageAbortFails(t *testing.T) {
	t.Parallel()

	session := expiredUploadOf(managev1.UploadType_UPLOAD_TYPE_USER_AVATAR)
	ports := newUploads(t, session)
	ports.abortErr = errors.New("storage unavailable")
	handlers := newTestHandlers(t, Deps{Uploads: ports, UploadStorage: ports})

	removed, err := handlers.cleanupExpiredUploadSession(t.Context(), session, time.Now(), time.Now())

	require.ErrorIs(t, err, ports.abortErr)
	require.False(t, removed)
	require.True(t, ports.aborted[session.UploadID], "the claim stays for the next run")
	require.True(t, ports.sessions[session.UploadID])
}

func TestCleanupExpiredUploadSessionTreatsMissingMultipartUploadAsClean(t *testing.T) {
	t.Parallel()

	session := expiredUploadOf(managev1.UploadType_UPLOAD_TYPE_USER_AVATAR)
	ports := newUploads(t, session)
	ports.abortErr = &smithy.GenericAPIError{Code: "NoSuchUpload", Message: "missing"}
	require.True(t, filemedia.IsMissingMultipartUploadAbortError(ports.abortErr))
	handlers := newTestHandlers(t, Deps{Uploads: ports, UploadStorage: ports})

	removed, err := handlers.cleanupExpiredUploadSession(t.Context(), session, time.Now(), time.Now())

	require.NoError(t, err)
	require.True(t, removed)
	require.Empty(t, ports.sessions)
}

func TestCleanupExpiredUploadSessionLeavesASessionAnotherCleanupClaimed(t *testing.T) {
	session := expiredUploadOf(managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE)
	ports := newUploads(t, session)
	ports.stolen[session.UploadID] = true
	handlers := newTestHandlers(t, Deps{Uploads: ports, UploadStorage: ports})

	removed, err := handlers.cleanupExpiredUploadSession(t.Context(), session, time.Now(), time.Now())

	require.NoError(t, err)
	require.False(t, removed)
	require.Equal(t, []string{"claim " + session.UploadID}, ports.calls.all(), "no storage call, no lifecycle event")
}

func TestCleanupExpiredUploadSessionFailsWhenTheSessionChangedAfterTheClaim(t *testing.T) {
	session := expiredUploadOf(managev1.UploadType_UPLOAD_TYPE_USER_AVATAR)
	ports := newUploads(t, session)
	ports.changed[session.UploadID] = true
	handlers := newTestHandlers(t, Deps{Uploads: ports, UploadStorage: ports})

	removed, err := handlers.cleanupExpiredUploadSession(t.Context(), session, time.Now(), time.Now())

	require.EqualError(t, err, "expired upload session "+session.UploadID+" changed before deletion")
	require.False(t, removed)
}

func TestCleanupExpiredUploadSessionRejectsASessionWithoutStorageIdentity(t *testing.T) {
	session := expiredUploadOf(managev1.UploadType_UPLOAD_TYPE_USER_AVATAR)
	session.FileID = " "
	handlers := newTestHandlers(t, Deps{})

	removed, err := handlers.cleanupExpiredUploadSession(t.Context(), session, time.Now(), time.Now())

	require.EqualError(t, err, "expired upload session is missing its storage identity")
	require.False(t, removed)
}

func TestHandleCleanupIncompleteUploadsSelectsByTheRetentionCutoff(t *testing.T) {
	ports := newUploads(t)
	handlers := newTestHandlers(t, Deps{Uploads: ports})
	now := time.Now()

	require.NoError(t, handlers.handleCleanupIncompleteUploads(t.Context(), now))
	require.Equal(t, []time.Time{now.Add(-filemedia.IncompleteUploadRetention)}, ports.cutoffs)
}

func TestHandleCleanupIncompleteUploadsGoesOnPastAFailedSessionAndLogsItOnce(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	failing := expiredUploadOf(managev1.UploadType_UPLOAD_TYPE_USER_AVATAR)
	cleaned := expiredUploadOf(managev1.UploadType_UPLOAD_TYPE_USER_AVATAR)
	ports := newUploads(t, failing, cleaned)
	ports.claimErr = nil
	storage := &failingAbort{uploads: ports, failing: failing.UploadID}
	handlers := newTestHandlers(t, Deps{Uploads: ports, UploadStorage: storage})

	err := handlers.handleCleanupIncompleteUploads(t.Context(), time.Now())

	// The scheduler logs the returned summary; the cause is logged per session.
	require.EqualError(t, err, "1 of 2 expired upload sessions were not cleaned up")
	require.Equal(t, map[string]bool{failing.UploadID: true}, ports.sessions, "the next session is cleaned")
	var entries []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(output.String()), "\n") {
		var entry map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &entry))
		if entry["level"] == "ERROR" {
			entries = append(entries, entry)
		}
	}
	require.Len(t, entries, 1)
	require.Equal(t, "Expired upload session cleanup failed", entries[0]["msg"])
	require.Equal(t, failing.UploadID, entries[0]["upload_id"])
	require.Equal(t, failing.FileID, entries[0]["file_id"])
	require.Contains(t, entries[0]["failure_cause"], "abort expired multipart upload "+failing.UploadID)
}

// failingAbort fails the multipart abort of one upload.
type failingAbort struct {
	*uploads
	failing string
}

func (f *failingAbort) AbortMultipartUpload(ctx context.Context, objectKey, uploadID string) error {
	if uploadID == f.failing {
		return errors.New("storage unavailable")
	}
	return f.uploads.AbortMultipartUpload(ctx, objectKey, uploadID)
}
