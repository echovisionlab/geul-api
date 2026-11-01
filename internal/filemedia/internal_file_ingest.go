package filemedia

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
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
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var attachErr error
		attachment, attachErr = s.attachTrackOriginalAudioWithDB(ctx, tx, target)
		return attachErr
	})
	if err != nil {
		return TrackOriginalAudioAttachment{}, err
	}
	return attachment, nil
}

func (s *Service) attachTrackOriginalAudioWithDB(ctx context.Context, tx *gorm.DB, target trackOriginalAudioTarget) (TrackOriginalAudioAttachment, error) {
	if err := s.trackAttachment.LockExistsWithDB(ctx, tx, target.trackID); err != nil {
		return TrackOriginalAudioAttachment{}, err
	}
	if err := mediaasset.LockAttachableFilesForUpdate(ctx, tx, []string{target.fileID}); err != nil {
		return TrackOriginalAudioAttachment{}, err
	}
	file, err := loadTrackOriginalAudioFile(tx, target)
	if err != nil {
		return TrackOriginalAudioAttachment{}, err
	}
	ready, err := clientMediaFileReady(ctx, tx, file)
	if err != nil {
		return TrackOriginalAudioAttachment{}, err
	}
	if file.ClientMediaBundleID != nil && !ready {
		return TrackOriginalAudioAttachment{}, domainerr.Precondition("client media derivatives are not ready")
	}
	attachment, err := s.trackAttachment.AttachOriginalWithDB(ctx, tx, TrackOriginalAudioInput{
		TrackID: target.trackID, VerifiedFileID: target.fileID, ExpectedCurrentFileID: target.expectedCurrentFileID,
		ClientMediaReady: ready, DurationSeconds: file.DurationSeconds,
	})
	if err != nil || attachment.AlreadyApplied || ready {
		return attachment, err
	}
	return attachment, s.enqueueTrackOriginalAudioTranscode(ctx, tx, file, target.trackID)
}

// loadTrackOriginalAudioFile reads the locked verified File and checks that
// its ingest attempt and binding belong to the Track.
func loadTrackOriginalAudioFile(tx *gorm.DB, target trackOriginalAudioTarget) (model.File, error) {
	var file model.File
	if err := tx.Where("id = ?", target.fileID).Take(&file).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
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
	if _, err := CanonicalMediaObjectTargetForFile(file); err != nil {
		return file, domainerr.Precondition("verified File metadata is not canonical").WithCause(failurecause.ServerFault(failurecause.WithAttrs(err, slog.String("file_id", file.ID))))
	}
	var binding model.FileIngestBinding
	if err := tx.Where("file_id = ?", target.fileID).Take(&binding).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
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

func (s *Service) enqueueTrackOriginalAudioTranscode(ctx context.Context, tx *gorm.DB, file model.File, trackID string) error {
	job, shouldEnqueue, err := newStableFileIngestAudioTranscodeJob(
		ctx, tx, file, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK, trackID,
	)
	if err != nil || !shouldEnqueue {
		return err
	}
	registrar, err := s.fileIngestTranscodeJobRegistrar()
	if err != nil {
		return err
	}
	if err := registrar.RegisterTranscodeAudio(ctx, tx, job); err != nil {
		return err
	}
	return publishDurableProtoInTransaction(
		ctx, s.transcodeCommandPublisher(), tx, eventpkg.QueueTranscoderAudio, job.GetEventId(), job,
	)
}
