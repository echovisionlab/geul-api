package filemedia

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (s *FileService) createUploadSession(ctx context.Context, session *model.UploadSession) error {
	if session == nil {
		return fmt.Errorf("upload session is required")
	}
	if session.UploadType == managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE.String() {
		return s.db.WithContext(ctx).Omit("EntityID").Create(session).Error
	}
	if session.UploadType == managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO.String() {
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			trackAttachment, dependencyErr := requireTrackAttachment(s.trackAttachment)
			if dependencyErr != nil {
				return dependencyErr
			}
			if err := trackAttachment.LockExistsWithDB(ctx, tx, session.EntityID); err != nil {
				return err
			}
			return tx.Create(session).Error
		})
	}

	uploadTypeValue, ok := managev1.UploadType_value[session.UploadType]
	if !ok || !isEditorFileIngestUploadType(managev1.UploadType(uploadTypeValue)) {
		return s.db.WithContext(ctx).Create(session).Error
	}
	if err := s.db.WithContext(ctx).
		Omit("EntityID", "EntityType", "SlotID", "ExpectedFileID").
		Create(session).Error; err != nil {
		return err
	}
	return nil
}

func (s *FileService) refreshUploadSessionActivity(ctx context.Context, uploadID string) {
	now := time.Now()
	if err := s.db.WithContext(ctx).
		Model(&model.UploadSession{}).
		Where("upload_id = ?", uploadID).
		Updates(structured.Fields{
			"last_activity_at": now,
			"updated_at":       now,
		}).Error; err != nil {
		slog.Warn("Failed to refresh upload session activity", "error", err, "uploadId", uploadID)
	}
}

func (s *FileService) deleteCompletedUploadSession(ctx context.Context, uploadID string) error {
	result := s.db.WithContext(ctx).
		Where("upload_id = ? AND status = ?", uploadID, model.UploadSessionStatusFinalizing).
		Delete(&model.UploadSession{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("completed upload session changed before deletion")
	}
	return nil
}

func (s *FileService) deleteAbortedUploadSession(ctx context.Context, uploadID string) error {
	result := s.db.WithContext(ctx).
		Where("upload_id = ? AND status = ?", uploadID, model.UploadSessionStatusAborted).
		Delete(&model.UploadSession{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("aborted upload session changed before deletion")
	}
	return nil
}

func (s *FileService) claimUploadSessionAbort(
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
			[]model.UploadSessionStatus{
				model.UploadSessionStatusInitiated,
				model.UploadSessionStatusUploading,
				model.UploadSessionStatusFailed,
				model.UploadSessionStatusAborted,
			},
		).
		Updates(structured.Fields{
			"status":           model.UploadSessionStatusAborted,
			"last_activity_at": now,
			"updated_at":       now,
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

func containsFinalizingUploadSession(sessions []model.UploadSession) bool {
	for _, session := range sessions {
		if session.Status == model.UploadSessionStatusFinalizing {
			return true
		}
	}
	return false
}
