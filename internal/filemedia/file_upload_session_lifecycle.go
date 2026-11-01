package filemedia

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (s *Service) createUploadSession(ctx context.Context, session *domain.UploadSession) error {
	if session == nil {
		return fmt.Errorf("upload session is required")
	}
	mode := domain.UploadTargetUnknown
	switch {
	case session.UploadType == managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE.String():
		mode = domain.UploadTargetGeneral
	case session.UploadType == managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO.String():
		mode = domain.UploadTargetTrackProjection
	default:
		if value, ok := managev1.UploadType_value[session.UploadType]; ok && isEditorFileIngestUploadType(managev1.UploadType(value)) {
			mode = domain.UploadTargetEditorFile
		}
	}
	return s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
		store := unit.Store()
		if mode == domain.UploadTargetTrackProjection {
			if err := store.RequireUploadTrack(ctx, session.EntityID); err != nil {
				return err
			}
		}
		row, err := store.InsertUploadSession(ctx, *session, mode)
		if err == nil {
			*session = row
		}
		return err
	})
}

func (s *Service) refreshUploadSessionActivity(ctx context.Context, uploadID string) {
	now := time.Now()
	if err := s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error { return unit.Store().RenewUploadActivity(ctx, uploadID, now) }); err != nil {
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

func containsFinalizingUploadSession(sessions []domain.UploadSession) bool {
	for _, session := range sessions {
		if session.Status == domain.UploadSessionStatusFinalizing {
			return true
		}
	}
	return false
}
