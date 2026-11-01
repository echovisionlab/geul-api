package filemediapostgres

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"gorm.io/gorm"
)

func (s *store) IngestFile(ctx context.Context, id string) (domain.File, error) {
	var row model.File
	err := s.tx.WithContext(ctx).Where("id = ?", id).Take(&row).Error
	return domain.File(row), fileError(err)
}

func (s *store) HasFileUploadSessions(ctx context.Context, id string) (bool, error) {
	var count int64
	err := s.tx.WithContext(ctx).Model(&model.UploadSession{}).Where("file_id = ?", id).Count(&count).Error
	return count != 0, err
}

func (s *store) IngestBinding(ctx context.Context, id string) (domain.IngestBinding, error) {
	var row model.FileIngestBinding
	err := s.tx.WithContext(ctx).Where("file_id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = storeError{err, domain.ErrIngestBindingNotFound}
	}
	return domain.IngestBinding(row), err
}

func (s *store) FailFinalizingUpload(ctx context.Context, id string, now time.Time) error {
	return s.tx.WithContext(ctx).Model(&model.UploadSession{}).Where("upload_id = ? AND status = ?", id, model.UploadSessionStatusFinalizing).Updates(structured.Fields{"status": model.UploadSessionStatusFailed, "last_activity_at": now, "updated_at": now}).Error
}
