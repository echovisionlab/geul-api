package filemediapostgres

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func writableUploadStatuses() []model.UploadSessionStatus {
	return []model.UploadSessionStatus{model.UploadSessionStatusInitiated, model.UploadSessionStatusUploading}
}

func (s *store) PrepareUploadBundle(ctx context.Context, upload, file, bundle, manifest string, now time.Time) (bool, error) {
	r := s.tx.WithContext(ctx).Model(&model.UploadSession{}).Where("upload_id = ? AND file_id = ? AND status IN ? AND client_media_bundle_id IS NULL", upload, file, writableUploadStatuses()).Updates(structured.Fields{"client_media_bundle_id": bundle, "client_media_manifest": manifest, "last_activity_at": now})
	return r.RowsAffected == 1, r.Error
}
func (s *store) WritableUploadSession(ctx context.Context, upload, file string) (domain.UploadSession, error) {
	var row model.UploadSession
	err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Where("upload_id = ? AND file_id = ? AND status IN ?", upload, file, writableUploadStatuses()).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = storeError{err, domain.ErrUploadSessionNotFound}
	}
	return UploadSession(row), err
}
func (s *store) MarkUploadPartUploading(ctx context.Context, upload, file string, now time.Time) (bool, error) {
	r := s.tx.WithContext(ctx).Model(&model.UploadSession{}).Where("upload_id = ? AND file_id = ? AND status IN ?", upload, file, writableUploadStatuses()).Updates(structured.Fields{"status": model.UploadSessionStatusUploading, "last_activity_at": now, "updated_at": now})
	return r.RowsAffected == 1, r.Error
}
func (s *store) UpsertUploadPart(ctx context.Context, input domain.UploadPart) error {
	row := model.UploadPart(input)
	return s.tx.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "upload_id"}, {Name: "part_number"}}, DoUpdates: clause.AssignmentColumns([]string{"etag", "size", "updated_at"})}).Create(&row).Error
}
func (s *store) DeleteUploadPart(ctx context.Context, upload string, part int32) error {
	return s.tx.WithContext(ctx).Where("upload_id = ? AND part_number = ?", upload, part).Delete(&model.UploadPart{}).Error
}
func (s *store) ReplaceUploadParts(ctx context.Context, upload string, input []domain.UploadPart) error {
	if err := s.tx.WithContext(ctx).Where("upload_id = ?", upload).Delete(&model.UploadPart{}).Error; err != nil {
		return err
	}
	if len(input) == 0 {
		return nil
	}
	rows := make([]model.UploadPart, len(input))
	for i, r := range input {
		rows[i] = model.UploadPart(r)
	}
	return s.tx.WithContext(ctx).Create(&rows).Error
}
func (s *store) DetectUploadMIME(ctx context.Context, upload, file, mime string, now time.Time) (bool, error) {
	r := s.tx.WithContext(ctx).Model(&model.UploadSession{}).Where("upload_id = ? AND file_id = ? AND status IN ? AND (detected_mime IS NULL OR detected_mime = '')", upload, file, writableUploadStatuses()).Updates(structured.Fields{"detected_mime": mime, "verified_at": now, "updated_at": now})
	return r.RowsAffected == 1, r.Error
}
func (s *store) UploadMIMEState(ctx context.Context, upload, file string) (domain.UploadMIMEState, error) {
	var row model.UploadSession
	err := s.tx.WithContext(ctx).Select("upload_id", "file_id", "status", "detected_mime").Where("upload_id = ? AND file_id = ?", upload, file).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = storeError{err, domain.ErrUploadSessionNotFound}
	}
	return domain.UploadMIMEState{Status: domain.UploadSessionStatus(row.Status), DetectedMime: row.DetectedMime}, err
}
