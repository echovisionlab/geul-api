package release

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	releasedomain "github.com/echovisionlab/geul-api/internal/release"
	releasepostgres "github.com/echovisionlab/geul-api/internal/release/postgres"
	"github.com/echovisionlab/geul-api/internal/transcode"
	transcodepostgres "github.com/echovisionlab/geul-api/internal/transcode/postgres"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

// TrackFileManager is the File domain's Track upload lifecycle.
type TrackFileManager interface {
	CleanupTrackUploadSessions(ctx context.Context, trackID string, reason string) error
	RequireNoTrackUploadSessionsWithDB(ctx context.Context, tx *gorm.DB, trackID string) error
}

type TrackFiles struct {
	files TrackFileManager
}

func NewTrackFiles(files TrackFileManager) *TrackFiles {
	if files == nil {
		panic("Release Track files are required")
	}
	return &TrackFiles{files: files}
}

func (f *TrackFiles) CleanupTrackUploadSessions(ctx context.Context, trackID, reason string) error {
	err := f.files.CleanupTrackUploadSessions(ctx, trackID, reason)
	if errors.Is(err, mediaasset.ErrUploadSessionNotAbortable) {
		return releasedomain.ErrTrackUploadSessionNotAbortable
	}
	return err
}

func (f *TrackFiles) RequireNoTrackUploadSessionsWithDB(
	ctx context.Context,
	tx *gorm.DB,
	trackID string,
) error {
	return f.files.RequireNoTrackUploadSessionsWithDB(ctx, tx, trackID)
}

type WaveformJobs struct {
	db        *gorm.DB
	publisher transcode.WaveformCancelPublisher
}

type transcodeJobCanceller interface {
	MarkCancelled(context.Context, string, managev1.TranscodeCancelReason) error
}

type transcodeCancelPublisher interface {
	PublishTranscodeCancel(context.Context, *managev1.TranscodeCancelEvent) error
}

type TrackTranscodes struct {
	canceller transcodeJobCanceller
	publisher transcodeCancelPublisher
}

func NewTrackTranscodes(runtime any) *TrackTranscodes {
	canceller, _ := runtime.(transcodeJobCanceller)
	publisher, _ := runtime.(transcodeCancelPublisher)
	return &TrackTranscodes{canceller: canceller, publisher: publisher}
}

// CancelDeletedTrackFiles cancels the transcode work of a deleted Track's
// files, once per File.
func (r *TrackTranscodes) CancelDeletedTrackFiles(ctx context.Context, trackID string, fileIDs ...*string) {
	reason := managev1.TranscodeCancelReason_TRANSCODE_CANCEL_REASON_USER_DELETED
	seen := make(map[string]struct{}, len(fileIDs))
	for _, fileID := range fileIDs {
		if fileID == nil || *fileID == "" {
			continue
		}
		if _, ok := seen[*fileID]; ok {
			continue
		}
		seen[*fileID] = struct{}{}

		if r.canceller != nil {
			if err := r.canceller.MarkCancelled(ctx, *fileID, reason); err != nil {
				slog.WarnContext(ctx, "Failed to mark track transcode jobs cancelled", "track_id", trackID, "file_id", *fileID, "reason", reason.String(), failurecause.Attr(err))
			}
		}
		if r.publisher != nil {
			if err := r.publisher.PublishTranscodeCancel(ctx, &managev1.TranscodeCancelEvent{
				FileId: *fileID, EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK,
				EntityId: trackID, Reason: reason, TimestampMs: time.Now().UnixMilli(),
			}); err != nil {
				slog.WarnContext(ctx, "Failed to publish track transcode cancel event", "track_id", trackID, "file_id", *fileID, "reason", reason.String(), failurecause.Attr(err))
			}
		}
	}
}

func NewWaveformJobs(db *gorm.DB, publisher transcode.WaveformCancelPublisher) *WaveformJobs {
	if db == nil {
		panic("Release waveform job database is required")
	}
	return &WaveformJobs{db: db, publisher: publisher}
}

// CancelDeletedTracks cancels the persisted waveform work of deleted Tracks.
func (j *WaveformJobs) CancelDeletedTracks(ctx context.Context, trackIDs []string) error {
	return transcodepostgres.CancelTrackWaveformJobs(ctx, j.db, j.publisher, trackIDs, managev1.TranscodeCancelReason_TRANSCODE_CANCEL_REASON_USER_DELETED)
}

var _ releasedomain.TrackUploads = (*TrackFiles)(nil)
var _ releasepostgres.TrackFiles = (*TrackFiles)(nil)
var _ releasedomain.TrackTranscodes = (*TrackTranscodes)(nil)
var _ releasedomain.WaveformJobs = (*WaveformJobs)(nil)
