package release

import (
	"context"
	"errors"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// ErrTrackUploadSessionNotAbortable is returned by the File boundary when a
// Track upload has entered finalization and can no longer be removed safely.
var ErrTrackUploadSessionNotAbortable = errors.New("track upload session is no longer abortable")

// TrackUploads aborts the in-flight audio uploads of a Track, outside any
// transaction. A Track deletion stops when an upload has begun finalizing
// (ErrTrackUploadSessionNotAbortable).
type TrackUploads interface {
	CleanupTrackUploadSessions(ctx context.Context, trackID string, reason string) error
}

// TrackTranscodes cancels the transcode work of a deleted Track's files.
type TrackTranscodes interface {
	CancelDeletedTrackFiles(ctx context.Context, trackID string, fileIDs ...*string)
}

// WaveformJobs cancels the persisted waveform work of deleted Tracks.
type WaveformJobs interface {
	CancelDeletedTracks(ctx context.Context, trackIDs []string) error
}

// MemberSummaryLoader projects foreign Member references without coupling the
// Release domain to the Member domain implementation.
type MemberSummaryLoader interface {
	LoadMemberSummaries(ctx context.Context, memberIDs []string) (map[string]*commonv1.MemberSummary, error)
}
