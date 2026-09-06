package release

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	releasedomain "github.com/echovisionlab/geul-api/internal/release"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type trackFilesStub struct {
	cleanupErr error
	trackID    string
	reason     string
}

func (*trackFilesStub) DeleteFileByID(context.Context, string) error { return nil }

func (s *trackFilesStub) CleanupTrackUploadSessions(_ context.Context, trackID, reason string) error {
	s.trackID = trackID
	s.reason = reason
	return s.cleanupErr
}

func (*trackFilesStub) RequireNoTrackUploadSessionsWithDB(context.Context, *gorm.DB, string) error {
	return nil
}

func TestTrackFilesMapsNonAbortableUploadError(t *testing.T) {
	files := &trackFilesStub{cleanupErr: mediaasset.ErrUploadSessionNotAbortable}
	adapter := NewTrackFiles(files)

	err := adapter.CleanupTrackUploadSessions(t.Context(), "track-1", "Track deleted")

	require.ErrorIs(t, err, releasedomain.ErrTrackUploadSessionNotAbortable)
	require.Equal(t, "track-1", files.trackID)
	require.Equal(t, "Track deleted", files.reason)
}

func TestTrackFilesPreservesOtherErrors(t *testing.T) {
	want := errors.New("cleanup failed")
	adapter := NewTrackFiles(&trackFilesStub{cleanupErr: want})

	err := adapter.CleanupTrackUploadSessions(t.Context(), "track-1", "Track deleted")

	require.ErrorIs(t, err, want)
}

type trackTranscodesStub struct {
	marked []string
	events []*managev1.TranscodeCancelEvent
}

func (s *trackTranscodesStub) MarkCancelled(
	_ context.Context,
	fileID string,
	_ managev1.TranscodeCancelReason,
) error {
	s.marked = append(s.marked, fileID)
	return nil
}

func (s *trackTranscodesStub) PublishTranscodeCancel(
	_ context.Context,
	event *managev1.TranscodeCancelEvent,
) error {
	s.events = append(s.events, event)
	return nil
}

func TestTrackTranscodesCancelsEachFileOnce(t *testing.T) {
	runtime := &trackTranscodesStub{}
	adapter := NewTrackTranscodes(runtime)
	fileID := "file-1"
	empty := ""

	adapter.CancelFiles(
		t.Context(),
		"track-1",
		managev1.TranscodeCancelReason_TRANSCODE_CANCEL_REASON_USER_DELETED,
		nil,
		&empty,
		&fileID,
		&fileID,
	)

	require.Equal(t, []string{"file-1"}, runtime.marked)
	require.Len(t, runtime.events, 1)
	event := runtime.events[0]
	require.Equal(t, "file-1", event.FileId)
	require.Equal(t, "track-1", event.EntityId)
	require.Equal(t, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK, event.EntityType)
	require.Equal(t, managev1.TranscodeCancelReason_TRANSCODE_CANCEL_REASON_USER_DELETED, event.Reason)
	require.NotZero(t, event.TimestampMs)
}
