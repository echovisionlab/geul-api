package filemedia

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	intrav1connect "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

type InternalFileIngestService struct {
	intrav1connect.UnimplementedInternalFileIngestServiceHandler
	files *FileService
}

func NewInternalFileIngestService(files *FileService) *InternalFileIngestService {
	if files == nil {
		panic("internal file ingest service: file service is required")
	}
	return &InternalFileIngestService{files: files}
}

func (s *InternalFileIngestService) AttachTrackOriginalAudio(
	ctx context.Context,
	req *connect.Request[intrav1.AttachTrackOriginalAudioRequest],
) (*connect.Response[intrav1.AttachTrackOriginalAudioResponse], error) {
	if req.Msg == nil {
		return nil, errs.Required("request")
	}
	attachment, err := s.files.attachTrackOriginalAudio(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&intrav1.AttachTrackOriginalAudioResponse{
		Result: attachment.result, CurrentFileId: attachment.currentFileID, ReleaseId: attachment.releaseID,
	}), nil
}

type trackOriginalAudioAttachment struct {
	result        intrav1.AttachTrackOriginalAudioResult
	currentFileID string
	releaseID     string
}

func (s *FileService) attachTrackOriginalAudio(
	ctx context.Context,
	req *intrav1.AttachTrackOriginalAudioRequest,
) (trackOriginalAudioAttachment, error) {
	if req == nil {
		return trackOriginalAudioAttachment{}, errs.Required("request")
	}
	trackID := strings.TrimSpace(req.GetTrackId())
	if _, err := uuid.Parse(trackID); err != nil {
		return trackOriginalAudioAttachment{}, errs.InvalidArgument("track_id", "must be a UUID")
	}
	fileID := strings.TrimSpace(req.GetVerifiedFileId())
	if _, err := uuid.Parse(fileID); err != nil {
		return trackOriginalAudioAttachment{}, errs.InvalidArgument("verified_file_id", "must be a UUID")
	}
	attemptID := strings.TrimSpace(req.GetIngestAttemptId())
	if _, err := uuid.Parse(attemptID); err != nil {
		return trackOriginalAudioAttachment{}, errs.InvalidArgument("ingest_attempt_id", "must be a UUID")
	}
	var expectedCurrentFileID *string
	if req.ExpectedCurrentFileId != nil {
		expected := strings.TrimSpace(req.GetExpectedCurrentFileId())
		if _, err := uuid.Parse(expected); err != nil {
			return trackOriginalAudioAttachment{}, errs.InvalidArgument("expected_current_file_id", "must be a UUID when present")
		}
		expectedCurrentFileID = &expected
	}

	var attachment TrackOriginalAudioAttachment
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		trackAttachment, dependencyErr := requireTrackAttachment(s.trackAttachment)
		if dependencyErr != nil {
			return dependencyErr
		}
		if err := trackAttachment.LockExistsWithDB(ctx, tx, trackID); err != nil {
			return err
		}
		if err := mediaasset.LockAttachableFilesForUpdate(ctx, tx, []string{fileID}); err != nil {
			return err
		}
		var file model.File
		if err := tx.Where("id = ?", fileID).Take(&file).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("file", fileID)
			}
			return err
		}
		if file.DeleteRequestedAt != nil {
			return errs.FailedPrecondition("verified File is pending deletion")
		}
		if file.IngestAttemptID == nil || strings.TrimSpace(*file.IngestAttemptID) != attemptID {
			return errs.FailedPrecondition("File ingest attempt does not match")
		}
		if _, err := CanonicalMediaObjectTargetForFile(file); err != nil {
			return errs.FailedPrecondition("verified File metadata is not canonical")
		}
		var binding model.FileIngestBinding
		if err := tx.Where("file_id = ?", fileID).Take(&binding).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.FailedPrecondition("File ingest binding is missing")
			}
			return err
		}
		trackEntityType := managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK.String()
		if binding.UploadType != managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO.String() ||
			binding.EntityID != trackID || binding.EntityType == nil || *binding.EntityType != trackEntityType {
			return errs.FailedPrecondition("File ingest binding does not match the Track")
		}

		var err error
		attachment, err = trackAttachment.AttachOriginalWithDB(ctx, tx, TrackOriginalAudioInput{
			TrackID: trackID, VerifiedFileID: fileID, ExpectedCurrentFileID: expectedCurrentFileID,
		})
		if err != nil || attachment.AlreadyApplied {
			return err
		}
		job, shouldEnqueue, err := newStableFileIngestAudioTranscodeJob(
			ctx, tx, file, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK, trackID,
		)
		if err != nil {
			return err
		}
		if shouldEnqueue {
			registrar, err := s.fileIngestTranscodeJobRegistrar()
			if err != nil {
				return err
			}
			if err := registrar.RegisterTranscodeAudio(ctx, tx, job); err != nil {
				return err
			}
			if err := publishDurableProtoInTransaction(
				ctx, s.transcodeCommandPublisher(), tx, eventpkg.QueueTranscoderAudio, job.GetEventId(), job,
			); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if _, ok := err.(*connect.Error); ok {
			return trackOriginalAudioAttachment{}, err
		}
		return trackOriginalAudioAttachment{}, errs.Internal(err)
	}
	result := intrav1.AttachTrackOriginalAudioResult_ATTACH_TRACK_ORIGINAL_AUDIO_RESULT_APPLIED
	if attachment.AlreadyApplied {
		result = intrav1.AttachTrackOriginalAudioResult_ATTACH_TRACK_ORIGINAL_AUDIO_RESULT_ALREADY_APPLIED
	}
	return trackOriginalAudioAttachment{
		result: result, currentFileID: attachment.CurrentFileID, releaseID: attachment.ReleaseID,
	}, nil
}
