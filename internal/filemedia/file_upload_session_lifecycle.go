package filemedia

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (s *Service) createUploadSession(ctx context.Context, session *model.UploadSession) error {
	if session == nil {
		return fmt.Errorf("upload session is required")
	}
	if session.UploadType == managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE.String() {
		return s.db.WithContext(ctx).Omit("EntityID").Create(session).Error
	}
	if session.UploadType == managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO.String() {
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			trackAttachment := s.trackAttachment
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

func (s *Service) refreshUploadSessionActivity(ctx context.Context, uploadID string) {
	now := time.Now()
	if err := s.db.WithContext(ctx).
		Model(&model.UploadSession{}).
		Where("upload_id = ?", uploadID).
		Updates(structured.Fields{
			"last_activity_at": now,
			"updated_at":       now,
		}).Error; err != nil {
		slog.ErrorContext(ctx, "Failed to refresh upload session activity", failurecause.Attr(err), "uploadId", uploadID)
	}
}

func (s *Service) deleteCompletedUploadSession(ctx context.Context, uploadID string) error {
	var deleted bool
	err := s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
		var err error
		deleted, err = unit.Store().DeleteFinalizingUpload(ctx, uploadID)
		return err
	})
	if err != nil {
		return err
	}
	if !deleted {
		return fmt.Errorf("completed upload session changed before deletion")
	}
	return nil
}
func (s *Service) deleteAbortedUploadSession(ctx context.Context, uploadID string) error {
	var deleted bool
	err := s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
		var err error
		deleted, err = unit.Store().DeleteAbortedUpload(ctx, uploadID)
		return err
	})
	if err != nil {
		return err
	}
	if !deleted {
		return fmt.Errorf("aborted upload session changed before deletion")
	}
	return nil
}
func (s *Service) claimUploadSessionAbort(ctx context.Context, uploadID, fileID string, now time.Time) (bool, error) {
	var claimed bool
	err := s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
		var err error
		claimed, err = unit.Store().ClaimUploadAborted(ctx, uploadID, fileID, now)
		return err
	})
	return claimed, err
}

func containsFinalizingUploadSession(sessions []model.UploadSession) bool {
	for _, session := range sessions {
		if session.Status == model.UploadSessionStatusFinalizing {
			return true
		}
	}
	return false
}
