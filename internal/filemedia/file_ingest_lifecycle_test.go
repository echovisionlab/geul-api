package filemedia

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

func randomFileIngestUUID() string {
	return uuid.NewString()
}

func TestFileIngestEventEmitterPublishesTypedEvents(t *testing.T) {
	t.Parallel()

	asyncPublisher := &capturingAsyncPublisher{}
	correlationID := randomFileIngestUUID()
	fileID := randomFileIngestUUID()
	fileKey := "media/" + fileID + ".mp3"
	emitter := newFileIngestEventEmitter(
		context.Background(),
		asyncPublisher,
		managev1.FileIngestSource_FILE_INGEST_SOURCE_REMOTE_URL,
		managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED,
		"",
		correlationID,
		fileID,
		100,
	)
	require.NotNil(t, emitter)

	emitter.setTarget(&commonv1.MediaObjectTarget{FileId: fileID, ObjectKey: fileKey, Extension: "mp3", MimeType: "audio/mpeg"})
	emitter.publishDownloadProgress(25)
	completedBytes := int64(100)
	emitter.publishFinalized(100, &completedBytes)

	downloadEvents := decodePublishedRoutedMessages(t, asyncPublisher.messages, eventpkg.SignalFileIngest, "", func() *managev1.FileIngestDownloadEvent {
		return &managev1.FileIngestDownloadEvent{}
	})
	require.Len(t, downloadEvents, 1)
	require.Equal(t, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_FILE, downloadEvents[0].GetIdentity().GetEntityType())
	require.Equal(t, fileID, downloadEvents[0].GetIdentity().GetEntityId())
	require.EqualValues(t, 1, downloadEvents[0].GetSequenceNumber())
	require.Equal(t, fileKey, downloadEvents[0].GetIdentity().GetTarget().GetObjectKey())
	require.EqualValues(t, 25, downloadEvents[0].GetProgress().GetPercentage())
	require.NotNil(t, downloadEvents[0].GetProgress().BytesCompleted)
	require.EqualValues(t, 25, *downloadEvents[0].GetProgress().BytesCompleted)
	require.NotNil(t, downloadEvents[0].GetProgress().BytesTotal)
	require.EqualValues(t, 100, *downloadEvents[0].GetProgress().BytesTotal)

	finalizedEvents := decodePublishedRoutedMessages(t, asyncPublisher.messages, eventpkg.SignalFileIngest, "", func() *managev1.FileIngestFinalizedEvent {
		return &managev1.FileIngestFinalizedEvent{}
	})
	require.Len(t, finalizedEvents, 1)
	require.EqualValues(t, 2, finalizedEvents[0].GetSequenceNumber())
	require.EqualValues(t, 100, finalizedEvents[0].GetProgress().GetPercentage())

}

func TestFileIngestMediaKindFromUploadTypeIncludesEditorMesh(t *testing.T) {
	t.Parallel()

	require.Equal(
		t,
		managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_EDITOR_MESH,
		fileIngestMediaKindFromUploadType(managev1.UploadType_UPLOAD_TYPE_EDITOR_MESH),
	)
}

func TestFileIngestEventEmitterUsesUploadSessionSequenceAllocator(t *testing.T) {
	t.Parallel()

	asyncPublisher := &capturingAsyncPublisher{}
	trackID := randomFileIngestUUID()
	fileID := randomFileIngestUUID()
	uploadID := randomFileIngestUUID()
	emitter := newFileIngestEventEmitter(
		context.Background(),
		asyncPublisher,
		managev1.FileIngestSource_FILE_INGEST_SOURCE_DIRECT_UPLOAD,
		managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK,
		trackID,
		"",
		fileID,
		100,
	)
	require.NotNil(t, emitter)

	nextSequence := int64(40)
	emitter.setTarget(&commonv1.MediaObjectTarget{
		FileId: fileID, ObjectKey: "media/" + fileID + ".ogg", Extension: "ogg", MimeType: "audio/ogg",
	})
	emitter.setUploadIdentity(model.UploadSession{
		UploadID:   uploadID,
		UploadType: managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO.String(),
	})
	emitter.setSequenceAllocator(func(context.Context, string) (int64, error) {
		nextSequence += 1
		return nextSequence, nil
	})

	completedBytes := int64(100)
	emitter.publishUploading(100, &completedBytes)
	require.NoError(t, emitter.publishAttachedConfirmed("source.ogg", "audio/ogg", 100))

	uploadEvents := decodePublishedRoutedMessages(t, asyncPublisher.messages, eventpkg.SignalFileIngest, "", func() *managev1.FileIngestUploadEvent {
		return &managev1.FileIngestUploadEvent{}
	})
	require.Len(t, uploadEvents, 1)
	require.EqualValues(t, 41, uploadEvents[0].GetSequenceNumber())

	attachedEvents := decodePublishedRoutedMessages(t, asyncPublisher.messages, eventpkg.SignalFileIngest, "", func() *managev1.FileIngestAttachedEvent {
		return &managev1.FileIngestAttachedEvent{}
	})
	require.Len(t, attachedEvents, 1)
	require.EqualValues(t, 42, attachedEvents[0].GetSequenceNumber())
}

func TestFileIngestEventEmitterSkipsDuplicateProgressWithinPublishInterval(t *testing.T) {
	t.Parallel()

	asyncPublisher := &capturingAsyncPublisher{}
	correlationID := randomFileIngestUUID()
	fileID := randomFileIngestUUID()
	emitter := newFileIngestEventEmitter(
		context.Background(),
		asyncPublisher,
		managev1.FileIngestSource_FILE_INGEST_SOURCE_REMOTE_URL,
		managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED,
		"",
		correlationID,
		fileID,
		100,
	)
	require.NotNil(t, emitter)

	emitter.publishDownloadProgress(10)
	emitter.publishDownloadProgress(10)

	events := decodePublishedRoutedMessages(t, asyncPublisher.messages, eventpkg.SignalFileIngest, "", func() *managev1.FileIngestDownloadEvent {
		return &managev1.FileIngestDownloadEvent{}
	})
	require.Len(t, events, 1)
	require.EqualValues(t, 10, events[0].GetProgress().GetPercentage())

	emitter.lastPublished = time.Now().Add(-fileIngestProgressPublishInterval)
	emitter.publishDownloadProgress(10)

	events = decodePublishedRoutedMessages(t, asyncPublisher.messages, eventpkg.SignalFileIngest, "", func() *managev1.FileIngestDownloadEvent {
		return &managev1.FileIngestDownloadEvent{}
	})
	require.Len(t, events, 2)
	require.EqualValues(t, 2, events[1].GetSequenceNumber())
	require.EqualValues(t, 10, events[1].GetProgress().GetPercentage())
}
