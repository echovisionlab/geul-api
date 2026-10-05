package release

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	releasedomain "github.com/echovisionlab/geul-api/internal/release"
	"github.com/echovisionlab/geul-api/internal/transcode"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

type TrackFiles struct {
	files releasedomain.TrackFileManager
}

func NewTrackFiles(files releasedomain.TrackFileManager) *TrackFiles {
	if files == nil {
		panic("Release Track files are required")
	}
	return &TrackFiles{files: files}
}

func (f *TrackFiles) DeleteFileByID(ctx context.Context, fileID string) error {
	return f.files.DeleteFileByID(ctx, fileID)
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

func (r *TrackTranscodes) CancelFiles(
	ctx context.Context,
	trackID string,
	reason managev1.TranscodeCancelReason,
	fileIDs ...*string,
) {
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
				slog.Warn("Failed to mark track transcode jobs cancelled", "trackId", trackID, "fileId", *fileID, "error", err)
			}
		}
		if r.publisher != nil {
			if err := r.publisher.PublishTranscodeCancel(ctx, &managev1.TranscodeCancelEvent{
				FileId: *fileID, EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK,
				EntityId: trackID, Reason: reason, TimestampMs: time.Now().UnixMilli(),
			}); err != nil {
				slog.Warn("Failed to publish track transcode cancel event", "trackId", trackID, "fileId", *fileID, "error", err)
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

func (j *WaveformJobs) CancelTracks(
	ctx context.Context,
	trackIDs []string,
	reason managev1.TranscodeCancelReason,
) error {
	return transcode.CancelTrackWaveformJobs(ctx, j.db, j.publisher, trackIDs, reason)
}

var _ releasedomain.TrackFileManager = (*TrackFiles)(nil)
var _ releasedomain.TrackTranscodes = (*TrackTranscodes)(nil)
var _ releasedomain.WaveformJobs = (*WaveformJobs)(nil)
