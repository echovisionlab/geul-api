package release

import (
	"context"
	"errors"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

// ErrTrackUploadSessionNotAbortable is returned by the File boundary when a
// Track upload has entered finalization and can no longer be removed safely.
var ErrTrackUploadSessionNotAbortable = errors.New("track upload session is no longer abortable")

// TrackFileManager owns completed Track audio files and in-flight Track audio
// uploads. Track deletion must stop when an upload has begun finalizing.
type TrackFileManager interface {
	DeleteFileByID(ctx context.Context, fileID string) error
	CleanupTrackUploadSessions(ctx context.Context, trackID string, reason string) error
	RequireNoTrackUploadSessionsWithDB(ctx context.Context, tx *gorm.DB, trackID string) error
}

// TrackTranscodes owns cancellation of Track file transcode work.
type TrackTranscodes interface {
	CancelFiles(context.Context, string, managev1.TranscodeCancelReason, ...*string)
}

// WaveformJobs owns cancellation of persisted Track waveform work.
type WaveformJobs interface {
	CancelTracks(context.Context, []string, managev1.TranscodeCancelReason) error
}

// Assets owns Release artwork attachment and ready public-asset projection.
type Assets interface {
	LockAttachableFiles(context.Context, *gorm.DB, []string) error
	BindArtwork(context.Context, *gorm.DB, string, string) (*commonv1.AssetRef, error)
	ReleaseArtwork(context.Context, *gorm.DB, string) error
	ReadyArtwork(context.Context, *gorm.DB, string) (*commonv1.AssetRef, error)
	ReadyAssets(context.Context, *gorm.DB, ...*string) (map[string]*commonv1.AssetRef, error)
}

// OG owns Release-generated Open Graph bindings and their cancellation state.
type OG interface {
	CancelAndRelease(context.Context, *gorm.DB, string) error
	LocaleAware() bool
}

// ContentOG requests current Release Open Graph work after content mutations.
type ContentOG interface {
	RequestCurrent(context.Context, *gorm.DB, string, string) error
}

// MemberSummaryLoader projects foreign Member references without coupling the
// Release domain to the Member domain implementation.
type MemberSummaryLoader interface {
	LoadMemberSummaries(ctx context.Context, memberIDs []string) (map[string]*commonv1.MemberSummary, error)
}

type ArtistSummary struct {
	Title string
	Slug  string
}

// ArtistSummaryLoader projects foreign Artist references without coupling the
// Release domain to Artist persistence.
type ArtistSummaryLoader interface {
	LoadArtistSummaries(ctx context.Context, artistIDs []string) (map[string]ArtistSummary, error)
	LoadArtistSummariesWithDB(ctx context.Context, db *gorm.DB, artistIDs []string) (map[string]ArtistSummary, error)
}

type LabelSummary struct {
	Title string
	Slug  string
}

// LabelSummaryLoader projects foreign Label references without coupling the
// Release domain to Label persistence.
type LabelSummaryLoader interface {
	LoadLabelSummaries(ctx context.Context, labelIDs []string) (map[string]LabelSummary, error)
	LoadLabelSummariesWithDB(ctx context.Context, db *gorm.DB, labelIDs []string) (map[string]LabelSummary, error)
}
