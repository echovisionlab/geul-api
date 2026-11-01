package filemedia

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// AttachTrackOriginalAudioCommand attaches a verified File as a Track's
// original audio. ExpectedCurrentFileID, when set, is the original the caller
// replaces.
type AttachTrackOriginalAudioCommand struct {
	TrackID               string
	VerifiedFileID        string
	IngestAttemptID       string
	ExpectedCurrentFileID *string
}

// trackOriginalAudioTarget is a validated AttachTrackOriginalAudioCommand.
type trackOriginalAudioTarget struct {
	trackID               string
	fileID                string
	attemptID             string
	expectedCurrentFileID *string
}

func validateTrackOriginalAudioTarget(command AttachTrackOriginalAudioCommand) (trackOriginalAudioTarget, error) {
	target := trackOriginalAudioTarget{
		trackID:   strings.TrimSpace(command.TrackID),
		fileID:    strings.TrimSpace(command.VerifiedFileID),
		attemptID: strings.TrimSpace(command.IngestAttemptID),
	}
	if _, err := uuid.Parse(target.trackID); err != nil {
		return target, domainerr.InvalidField("track_id", "must be a UUID")
	}
	if _, err := uuid.Parse(target.fileID); err != nil {
		return target, domainerr.InvalidField("verified_file_id", "must be a UUID")
	}
	if _, err := uuid.Parse(target.attemptID); err != nil {
		return target, domainerr.InvalidField("ingest_attempt_id", "must be a UUID")
	}
	if command.ExpectedCurrentFileID != nil {
		expected := strings.TrimSpace(*command.ExpectedCurrentFileID)
		if _, err := uuid.Parse(expected); err != nil {
			return target, domainerr.InvalidField("expected_current_file_id", "must be a UUID when present")
		}
		target.expectedCurrentFileID = &expected
	}
	return target, nil
}

// AttachTrackOriginalAudio is the collaboration service's Track audio ingest
// boundary.
func (s *Service) AttachTrackOriginalAudio(ctx context.Context, command AttachTrackOriginalAudioCommand) (TrackOriginalAudioAttachment, error) {
	target, err := validateTrackOriginalAudioTarget(command)
	if err != nil {
		return TrackOriginalAudioAttachment{}, err
	}
	var attachment TrackOriginalAudioAttachment
	err = s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
		var attachErr error
		attachment, attachErr = s.attachTrackOriginalAudio(ctx, unit.Store(), target)
		return attachErr
	})
	if err != nil {
		return TrackOriginalAudioAttachment{}, err
	}
	return attachment, nil
}

func (s *Service) attachTrackOriginalAudio(ctx context.Context, store Store, target trackOriginalAudioTarget) (TrackOriginalAudioAttachment, error) {
	if err := store.RequireUploadTrack(ctx, target.trackID); err != nil {
		return TrackOriginalAudioAttachment{}, err
	}
	if err := store.LockIngestFile(ctx, target.fileID); err != nil {
		return TrackOriginalAudioAttachment{}, err
	}
	file, err := loadTrackOriginalAudioFile(ctx, store, target)
	if err != nil {
		return TrackOriginalAudioAttachment{}, err
	}
	ready, err := clientMediaFileReady(ctx, store, file)
	if err != nil {
		return TrackOriginalAudioAttachment{}, err
	}
	if file.ClientMediaBundleID != nil && !ready {
		return TrackOriginalAudioAttachment{}, domainerr.Precondition("client media derivatives are not ready")
	}
	attachment, err := store.AttachIngestTrackAudio(ctx, TrackOriginalAudioInput{
		TrackID: target.trackID, VerifiedFileID: target.fileID, ExpectedCurrentFileID: target.expectedCurrentFileID,
		ClientMediaReady: ready, DurationSeconds: file.DurationSeconds,
	})
	if err != nil || attachment.AlreadyApplied || ready {
		return attachment, err
	}
	return attachment, s.enqueueTrackOriginalAudioTranscode(ctx, store, file, target.trackID)
}

// loadTrackOriginalAudioFile reads the locked verified File and checks that
// its ingest attempt and binding belong to the Track.
func loadTrackOriginalAudioFile(ctx context.Context, store Store, target trackOriginalAudioTarget) (File, error) {
	file, err := store.IngestFile(ctx, target.fileID)
	if err != nil {
		if errors.Is(err, domain.ErrFileNotFound) {
			return file, domainerr.ResourceNotFound("file", target.fileID)
		}
		return file, err
	}

	if file.DeleteRequestedAt != nil {
		return file, domainerr.Precondition("verified File is pending deletion")
	}
	if file.IngestAttemptID == nil || strings.TrimSpace(*file.IngestAttemptID) != target.attemptID {
		return file, domainerr.Precondition("File ingest attempt does not match")
	}
	if _, err := domain.CanonicalObjectTarget(file); err != nil {
		return file, domainerr.Precondition("verified File metadata is not canonical").WithCause(failurecause.ServerFault(failurecause.WithAttrs(err, slog.String("file_id", file.ID))))
	}
	binding, err := store.IngestBinding(ctx, target.fileID)
	if err != nil {
		if errors.Is(err, domain.ErrIngestBindingNotFound) {
			return file, domainerr.Precondition("File ingest binding is missing")
		}
		return file, err
	}

	trackEntityType := managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK.String()
	if binding.UploadType != managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO.String() ||
		binding.EntityID != target.trackID || binding.EntityType == nil || *binding.EntityType != trackEntityType {
		return file, domainerr.Precondition("File ingest binding does not match the Track")
	}
	return file, nil
}

func (s *Service) enqueueTrackOriginalAudioTranscode(ctx context.Context, store Store, file File, trackID string) error {
	job, shouldEnqueue, err := newStableFileIngestAudioTranscodeJob(
		ctx, store, file, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK, trackID,
	)
	if err != nil || !shouldEnqueue {
		return err
	}
	return store.EnqueueIngestTranscode(ctx, *job)
}
