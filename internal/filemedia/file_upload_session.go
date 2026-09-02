package filemedia

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const storageCompensationTimeout = 10 * time.Second

func newStorageCompensationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), storageCompensationTimeout)
}

func (s *FileService) recordInterruptedMultipartUpload(
	ctx context.Context,
	session model.UploadSession,
	uploadID string,
	logMessage string,
	readErr error,
) {
	fileKey, _ := uploadSessionObjectKey(session)
	slog.Warn(
		logMessage,
		"error", readErr,
		"fileKey", fileKey,
		"uploadId", session.UploadID,
		"partUploadId", uploadID,
	)

	cleanupCtx, cancel := newStorageCompensationContext(ctx)
	defer cancel()

	now := time.Now()
	if err := s.db.WithContext(cleanupCtx).
		Model(&model.UploadSession{}).
		Where("upload_id = ? AND file_id = ? AND status IN ?",
			session.UploadID,
			session.FileID,
			activeUploadSessionStatuses(),
		).
		Updates(structured.Fields{
			"last_activity_at": now,
			"updated_at":       now,
		}).Error; err != nil {
		slog.Warn(
			"Failed to refresh upload session activity after interruption",
			"error", err,
			"fileKey", fileKey,
			"uploadId", session.UploadID,
			"partUploadId", uploadID,
		)
	}
}

func uploadSessionMediaObjectTarget(session model.UploadSession) (*commonv1.MediaObjectTarget, error) {
	return CanonicalMediaObjectTargetForFile(model.File{
		ID:        session.FileID,
		Extension: mediaExtension(&session.RequestedMime),
		MimeType:  session.RequestedMime,
	})
}

func uploadSessionObjectKey(session model.UploadSession) (string, error) {
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

func (s *FileService) nextUploadSessionIngestSequence(ctx context.Context, uploadID string) (int64, error) {
	var sequence int64
	if err := s.db.WithContext(ctx).
		Raw(
			`UPDATE upload_session
			 SET ingest_sequence = ingest_sequence + 1
			 WHERE upload_id = ?
			 RETURNING ingest_sequence`,
			uploadID,
		).
		Row().
		Scan(&sequence); err != nil {
		return 0, fmt.Errorf("failed to allocate upload session ingest sequence: %w", err)
	}
	return sequence, nil
}

func (s *FileService) bindUploadSessionIngestEmitter(emitter *fileIngestEventEmitter, session model.UploadSession) error {
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

func uploadSessionStatusToProto(status model.UploadSessionStatus) managev1.UploadSessionStatus {
	switch status {
	case model.UploadSessionStatusInitiated:
		return managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_INITIATED
	case model.UploadSessionStatusUploading:
		return managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_UPLOADING
	case model.UploadSessionStatusFinalizing:
		return managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_FINALIZING
	case model.UploadSessionStatusFailed:
		return managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_FAILED
	case model.UploadSessionStatusAborted:
		return managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_ABORTED
	default:
		return managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_UNSPECIFIED
	}
}

func activeUploadSessionStatuses() []model.UploadSessionStatus {
	return []model.UploadSessionStatus{
		model.UploadSessionStatusInitiated,
		model.UploadSessionStatusUploading,
		model.UploadSessionStatusFinalizing,
	}
}

func uploadPartWritableSessionStatuses() []model.UploadSessionStatus {
	return []model.UploadSessionStatus{
		model.UploadSessionStatusInitiated,
		model.UploadSessionStatusUploading,
	}
}

func (s *FileService) claimUploadPartActivity(
	ctx context.Context,
	uploadID string,
	fileID string,
	now time.Time,
) (bool, error) {
	result := s.db.WithContext(ctx).
		Model(&model.UploadSession{}).
		Where(
			"upload_id = ? AND file_id = ? AND status IN ?",
			uploadID,
			fileID,
			uploadPartWritableSessionStatuses(),
		).
		Updates(structured.Fields{
			"last_activity_at": now,
			"updated_at":       now,
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

func (s *FileService) recordRetryableMultipartPartFailure(
	ctx context.Context,
	session model.UploadSession,
	now time.Time,
) error {
	return s.db.WithContext(ctx).
		Model(&model.UploadSession{}).
		Where(
			"upload_id = ? AND file_id = ? AND status IN ?",
			session.UploadID,
			session.FileID,
			uploadPartWritableSessionStatuses(),
		).
		Updates(structured.Fields{
			"last_activity_at": now,
			"updated_at":       now,
		}).Error
}

func (s *FileService) claimMultipartCompletion(
	ctx context.Context,
	session model.UploadSession,
	now time.Time,
) (bool, []model.UploadPart, error) {
	var claimed bool
	var uploadedParts []model.UploadPart
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, parts, lockedClaimed, err := claimLockedMultipartCompletion(ctx, tx, session, now)
		if err != nil {
			return err
		}
		claimed = lockedClaimed
		uploadedParts = parts
		return nil
	})
	return claimed, uploadedParts, err
}

func claimLockedMultipartCompletion(
	ctx context.Context,
	tx *gorm.DB,
	session model.UploadSession,
	now time.Time,
) (model.UploadSession, []model.UploadPart, bool, error) {
	var locked model.UploadSession
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("upload_id = ? AND file_id = ?", session.UploadID, session.FileID).
		Take(&locked).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return model.UploadSession{}, nil, false, nil
		}
		return model.UploadSession{}, nil, false, err
	}
	if locked.Status != model.UploadSessionStatusInitiated &&
		locked.Status != model.UploadSessionStatusUploading &&
		locked.Status != model.UploadSessionStatusFinalizing {
		return locked, nil, false, nil
	}

	var uploadedParts []model.UploadPart
	if err := tx.WithContext(ctx).
		Where("upload_id = ?", locked.UploadID).
		Order("part_number ASC").
		Find(&uploadedParts).Error; err != nil {
		return locked, nil, false, err
	}
	if err := validateMultipartCompletionParts(locked, uploadedParts); err != nil {
		return locked, uploadedParts, false, err
	}

	result := tx.Model(&model.UploadSession{}).
		Where(
			"upload_id = ? AND file_id = ? AND status IN ?",
			locked.UploadID,
			locked.FileID,
			activeUploadSessionStatuses(),
		).
		Updates(structured.Fields{
			"status":           model.UploadSessionStatusFinalizing,
			"last_activity_at": now,
			"updated_at":       now,
		})
	if result.Error != nil {
		return locked, uploadedParts, false, result.Error
	}
	return locked, uploadedParts, result.RowsAffected == 1, nil
}

func uploadSessionMatchesSelection(
	session model.UploadSession,
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
	session model.UploadSession,
	uploadedParts []*managev1.UploadPartInfo,
	resumed bool,
) (*managev1.InitiateMultipartUploadResponse, error) {
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
	}, nil
}

func (s *FileService) loadUploadPartInfos(ctx context.Context, uploadID string) ([]*managev1.UploadPartInfo, error) {
	return loadUploadPartInfosWithDB(ctx, s.db, uploadID)
}

func loadUploadPartInfosWithDB(
	ctx context.Context,
	db *gorm.DB,
	uploadID string,
) ([]*managev1.UploadPartInfo, error) {
	var uploadedParts []model.UploadPart
	if err := db.WithContext(ctx).
		Where("upload_id = ?", uploadID).
		Order("part_number ASC").
		Find(&uploadedParts).Error; err != nil {
		return nil, err
	}
	return uploadPartInfos(uploadedParts), nil
}
