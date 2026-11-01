package filemedia

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const storageCompensationTimeout = 10 * time.Second

func newStorageCompensationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), storageCompensationTimeout)
}

func (s *Service) recordInterruptedMultipartUpload(
	ctx context.Context,
	session domain.UploadSession,
	uploadID string,
	operation string,
	readErr error,
) {
	fileKey, _ := uploadSessionObjectKey(session)
	// The request ends with 408; its record keeps the interrupted read.
	failurecause.RecorderFrom(ctx).Record(failurecause.Of(failurecause.WithAttrs(
		fmt.Errorf("%s: %w", operation, readErr),
		slog.String("upload_id", session.UploadID), slog.String("file_id", session.FileID),
	)))

	cleanupCtx, cancel := newStorageCompensationContext(ctx)
	defer cancel()

	now := time.Now()
	if err := s.units.Transaction(cleanupCtx, false, func(unit mutation.Tx[Store]) error {
		_, err := unit.Store().RenewActiveUploadActivity(cleanupCtx, session.UploadID, session.FileID, false, now)
		return err
	}); err != nil {
		slog.ErrorContext(ctx, "Failed to refresh upload session activity after interruption",
			failurecause.Attr(err),
			"fileKey", fileKey,
			"uploadId", session.UploadID,
			"partUploadId", uploadID,
		)
	}
}

func uploadSessionMediaObjectTarget(session domain.UploadSession) (*domain.ObjectTarget, error) {
	return CanonicalObjectTarget(File{
		ID:        session.FileID,
		Extension: mediaExtension(&session.RequestedMime),
		MimeType:  session.RequestedMime,
	})
}

func uploadSessionObjectKey(session domain.UploadSession) (string, error) {
	target, err := uploadSessionMediaObjectTarget(session)
	if err != nil {
		return "", err
	}
	return target.ObjectKey, nil
}

func uploadSessionEntityTypeToEnum(entityType *string) managev1.TranscodeEntityType {
	if entityType == nil || *entityType == "" {
		return managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED
	}

	if value, ok := managev1.TranscodeEntityType_value[*entityType]; ok {
		return managev1.TranscodeEntityType(value)
	}

	return managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED
}

func (s *Service) nextUploadSessionIngestSequence(ctx context.Context, uploadID string) (int64, error) {
	var sequence int64
	if err := s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
		var err error
		sequence, err = unit.Store().NextUploadIngestSequence(ctx, uploadID)
		return err
	}); err != nil {
		return 0, fmt.Errorf("failed to allocate upload session ingest sequence: %w", err)
	}
	return sequence, nil
}

func (s *Service) bindUploadSessionIngestEmitter(emitter *fileIngestEventEmitter, session domain.UploadSession) error {
	if emitter == nil {
		return nil
	}
	target, err := uploadSessionMediaObjectTarget(session)
	if err != nil {
		return fmt.Errorf("restore upload session media target: %w", err)
	}
	emitter.setTarget(target)
	emitter.setUploadIdentity(session)
	ingestTarget, err := fileIngestTargetFromStoredSession(session)
	if err != nil {
		return fmt.Errorf("restore upload session ingest target: %w", err)
	}
	if err := emitter.setProjectionIdentity(ingestTarget); err != nil {
		return err
	}
	emitter.setSequenceAllocator(s.nextUploadSessionIngestSequence)
	return nil
}

func uploadSessionStatusToProto(status domain.UploadSessionStatus) managev1.UploadSessionStatus {
	switch status {
	case domain.UploadSessionStatusInitiated:
		return managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_INITIATED
	case domain.UploadSessionStatusUploading:
		return managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_UPLOADING
	case domain.UploadSessionStatusFinalizing:
		return managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_FINALIZING
	case domain.UploadSessionStatusFailed:
		return managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_FAILED
	case domain.UploadSessionStatusAborted:
		return managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_ABORTED
	default:
		return managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_UNSPECIFIED
	}
}

func (s *Service) claimUploadPartActivity(
	ctx context.Context,
	uploadID string,
	fileID string,
	now time.Time,
) (bool, error) {
	var claimed bool
	err := s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
		var err error
		claimed, err = unit.Store().RenewActiveUploadActivity(ctx, uploadID, fileID, true, now)
		return err
	})
	return claimed, err
}

func (s *Service) recordRetryableMultipartPartFailure(
	ctx context.Context,
	session domain.UploadSession,
	now time.Time,
) error {
	return s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
		_, err := unit.Store().RenewActiveUploadActivity(ctx, session.UploadID, session.FileID, true, now)
		return err
	})
}

func (s *Service) claimMultipartCompletion(
	ctx context.Context,
	session domain.UploadSession,
	now time.Time,
) (bool, []domain.UploadPart, error) {
	var claimed bool
	var uploadedParts []domain.UploadPart
	err := s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
		store := unit.Store()
		locked, err := store.UploadSession(ctx, session.UploadID, session.FileID, true)
		if errors.Is(err, domain.ErrUploadSessionNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if locked.Status != domain.UploadSessionStatusInitiated && locked.Status != domain.UploadSessionStatusUploading && locked.Status != domain.UploadSessionStatusFinalizing {
			return nil
		}
		rows, err := store.UploadParts(ctx, locked.UploadID)
		if err != nil {
			return err
		}
		parts := make([]domain.UploadPart, len(rows))
		copy(parts, rows)
		if err := validateMultipartCompletionParts(locked.FileSize, effectiveMultipartChunkSize(locked.ChunkSize), locked.TotalParts, parts); err != nil {
			return err
		}
		changed, err := store.ClaimUploadFinalizing(ctx, locked.UploadID, locked.FileID, now)
		if err != nil {
			return err
		}
		claimed = changed
		uploadedParts = parts
		return nil
	})
	return claimed, uploadedParts, err
}

func uploadSessionMatchesSelection(
	session domain.UploadSession,
	fileName string,
	fileSize int64,
	mimeType string,
	fileLastModified *int64,
) bool {
	if session.FileName != fileName || session.FileSize != fileSize || session.RequestedMime != mimeType {
		return false
	}

	if fileLastModified == nil {
		return true
	}
	if session.FileLastModified == nil {
		return false
	}
	return *session.FileLastModified == *fileLastModified
}

func multipartInitiateResponseFromSession(
	session domain.UploadSession,
	uploadedParts []*managev1.UploadPartInfo,
	resumed bool,
) *managev1.InitiateMultipartUploadResponse {
	return &managev1.InitiateMultipartUploadResponse{
		UploadId:              session.UploadID,
		FileId:                session.FileID,
		Extension:             mediaExtension(&session.RequestedMime),
		TotalParts:            session.TotalParts,
		ChunkSize:             session.ChunkSize,
		UploadedParts:         uploadedParts,
		Status:                uploadSessionStatusToProto(session.Status),
		Resumed:               resumed,
		SlotId:                session.SlotID,
		IngestAttemptId:       session.AttemptID,
		ExpectedCurrentFileId: session.ExpectedFileID,
	}
}

func (s *Service) loadUploadPartInfos(ctx context.Context, uploadID string) ([]*managev1.UploadPartInfo, error) {
	var rows []domain.UploadPart
	err := s.units.Read(ctx, func(store Store) error { var err error; rows, err = store.UploadParts(ctx, uploadID); return err })
	if err != nil {
		return nil, err
	}
	parts := make([]domain.UploadPart, len(rows))
	copy(parts, rows)
	return uploadPartInfos(parts), nil
}
