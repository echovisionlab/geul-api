package filemedia

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// IncompleteUploadRetention is the maximum idle lifetime of an unfinished
// multipart upload session before the FileMedia cleanup consumer may abort it.
const IncompleteUploadRetention = 7 * 24 * time.Hour

func (s *FileService) cleanupSupersededUploadSessions(
	ctx context.Context,
	sessions []model.UploadSession,
	reason string,
) error {
	if len(sessions) == 0 {
		return nil
	}

	if containsFinalizingUploadSession(sessions) {
		return mediaasset.ErrUploadSessionNotAbortable
	}

	for _, session := range sessions {
		if err := s.abortUploadSession(ctx, session, reason); err != nil {
			return err
		}
	}

	return nil
}

func (s *FileService) abortUploadSession(
	ctx context.Context,
	session model.UploadSession,
	reason string,
) error {
	if strings.TrimSpace(session.UploadID) == "" {
		return nil
	}
	claimed, err := s.claimUploadSessionAbort(ctx, session.UploadID, session.FileID, time.Now())
	if err != nil {
		return fmt.Errorf("claim upload session abort %s: %w", session.UploadID, err)
	}
	if !claimed {
		return fmt.Errorf("%w: %s", mediaasset.ErrUploadSessionNotAbortable, session.UploadID)
	}
	fileKey, err := uploadSessionObjectKey(session)
	if err != nil {
		return fmt.Errorf("invalid upload session %s: %w", session.UploadID, err)
	}
	cleanupCtx, cancel := newStorageCompensationContext(ctx)
	defer cancel()
	_, abortErr := s.s3Client.AbortMultipartUpload(cleanupCtx, &s3.AbortMultipartUploadInput{
		Bucket:   aws.String(s.s3Bucket),
		Key:      aws.String(fileKey),
		UploadId: aws.String(session.UploadID),
	})
	if abortErr != nil {
		if IsMissingMultipartUploadAbortError(abortErr) {
			slog.Info("Upload session was already absent in object storage", "uploadId", session.UploadID, "fileId", session.FileID)
		} else {
			slog.Warn("Failed to abort upload session", "uploadId", session.UploadID, "fileId", session.FileID, "error", abortErr)
			return fmt.Errorf("failed to abort upload session %s: %w", session.UploadID, abortErr)
		}
	}
	progressEmitter := newFileIngestEventEmitter(
		ctx,
		s.asyncPublisher,
		managev1.FileIngestSource_FILE_INGEST_SOURCE_DIRECT_UPLOAD,
		uploadSessionEntityTypeToEnum(session.EntityType),
		session.EntityID,
		"",
		session.FileID,
		session.FileSize,
	)
	if progressEmitter != nil {
		if err := s.bindUploadSessionIngestEmitter(progressEmitter, session); err != nil {
			return err
		}
		progressEmitter.publishFailed(reason, 0, nil)
	}
	if err := s.deleteAbortedUploadSession(cleanupCtx, session.UploadID); err != nil {
		return fmt.Errorf("delete aborted upload session %s: %w", session.UploadID, err)
	}
	return nil
}

// CleanupTrackUploadSessions removes only abortable Track audio sessions. A
// finalizing session is completion authority and blocks Track deletion.
func (s *FileService) CleanupTrackUploadSessions(
	ctx context.Context,
	trackID string,
	reason string,
) error {
	trackID = strings.TrimSpace(trackID)
	if trackID == "" {
		return nil
	}

	sessions, err := s.claimTrackUploadSessionsForCleanup(ctx, trackID)
	if err != nil {
		return fmt.Errorf("find Track audio upload sessions: %w", err)
	}
	return s.cleanupSupersededUploadSessions(ctx, sessions, reason)
}

func (s *FileService) claimTrackUploadSessionsForCleanup(ctx context.Context, trackID string) ([]model.UploadSession, error) {
	var sessions []model.UploadSession
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		trackAttachment, dependencyErr := requireTrackAttachment(s.trackAttachment)
		if dependencyErr != nil {
			return dependencyErr
		}
		if err := trackAttachment.LockExistsWithDB(ctx, tx, trackID); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("upload_type = ? AND entity_id = ?", managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO.String(), trackID).
			Find(&sessions).Error; err != nil {
			return err
		}
		if containsFinalizingUploadSession(sessions) {
			return mediaasset.ErrUploadSessionNotAbortable
		}
		if len(sessions) == 0 {
			return nil
		}
		uploadIDs := make([]string, 0, len(sessions))
		for _, session := range sessions {
			uploadIDs = append(uploadIDs, session.UploadID)
		}
		now := time.Now()
		result := tx.Model(&model.UploadSession{}).
			Where("upload_id IN ? AND status IN ?", uploadIDs, []model.UploadSessionStatus{
				model.UploadSessionStatusInitiated,
				model.UploadSessionStatusUploading,
				model.UploadSessionStatusFailed,
				model.UploadSessionStatusAborted,
			}).
			Updates(structured.Fields{
				"status": model.UploadSessionStatusAborted, "last_activity_at": now, "updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != int64(len(sessions)) {
			return mediaasset.ErrUploadSessionNotAbortable
		}
		return nil
	})
	return sessions, err
}

// RequireNoTrackUploadSessionsWithDB enforces the File-owned upload-session
// invariant inside the Release caller's Track-deletion transaction.
func (s *FileService) RequireNoTrackUploadSessionsWithDB(ctx context.Context, tx *gorm.DB, trackID string) error {
	var count int64
	if err := tx.WithContext(ctx).Model(&model.UploadSession{}).
		Where("upload_type = ? AND entity_id = ?", managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO.String(), trackID).
		Count(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return ErrTrackUploadSessionsChanged
	}
	return nil
}

func (s *FileService) publishTranscodeCancelIfSupported(
	ctx context.Context,
	fileID string,
	entityType managev1.TranscodeEntityType,
	entityID string,
	reason managev1.TranscodeCancelReason,
) {
	event := &managev1.TranscodeCancelEvent{
		FileId:      fileID,
		EntityType:  entityType,
		EntityId:    entityID,
		Reason:      reason,
		TimestampMs: time.Now().UnixMilli(),
	}
	type transcodeCancelPublisher interface {
		PublishTranscodeCancel(context.Context, *managev1.TranscodeCancelEvent) error
	}
	if publisher, ok := s.publisher.(transcodeCancelPublisher); ok {
		if err := publisher.PublishTranscodeCancel(ctx, event); err != nil {
			slog.Warn("Failed to publish editor file transcode cancel event", "error", err, "fileId", fileID)
		}
	}
	type transcodeJobCanceller interface {
		MarkCancelled(context.Context, string, managev1.TranscodeCancelReason) error
	}
	if canceller, ok := s.publisher.(transcodeJobCanceller); ok {
		if err := canceller.MarkCancelled(ctx, fileID, reason); err != nil {
			slog.Warn("Failed to mark editor file transcode job cancelled", "error", err, "fileId", fileID)
		}
	}
}
