package filemediapostgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func UploadSession(r model.UploadSession) domain.UploadSession {
	return domain.UploadSession{UploadID: r.UploadID, ClientMediaBundleID: r.ClientMediaBundleID, ClientMediaManifest: r.ClientMediaManifest, FileID: r.FileID, UploadType: r.UploadType, EntityID: r.EntityID, EntityType: r.EntityType, FileName: r.FileName, FileSize: r.FileSize, FileLastModified: r.FileLastModified, SlotID: r.SlotID, AttemptID: r.AttemptID, ExpectedFileID: r.ExpectedFileID, IngestSequence: r.IngestSequence, RequestedMime: r.RequestedMime, DetectedMime: r.DetectedMime, TotalParts: r.TotalParts, ChunkSize: r.ChunkSize, Status: domain.UploadSessionStatus(r.Status), VerifiedAt: r.VerifiedAt, LastActivityAt: r.LastActivityAt, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}
func UploadSessionRow(r domain.UploadSession) model.UploadSession {
	return model.UploadSession{UploadID: r.UploadID, ClientMediaBundleID: r.ClientMediaBundleID, ClientMediaManifest: r.ClientMediaManifest, FileID: r.FileID, UploadType: r.UploadType, EntityID: r.EntityID, EntityType: r.EntityType, FileName: r.FileName, FileSize: r.FileSize, FileLastModified: r.FileLastModified, SlotID: r.SlotID, AttemptID: r.AttemptID, ExpectedFileID: r.ExpectedFileID, IngestSequence: r.IngestSequence, RequestedMime: r.RequestedMime, DetectedMime: r.DetectedMime, TotalParts: r.TotalParts, ChunkSize: r.ChunkSize, Status: model.UploadSessionStatus(r.Status), VerifiedAt: r.VerifiedAt, LastActivityAt: r.LastActivityAt, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}
func (s *store) UploadSession(ctx context.Context, upload, file string, lock bool) (domain.UploadSession, error) {
	var row model.UploadSession
	q := s.tx.WithContext(ctx).Where("upload_id = ? AND file_id = ?", upload, file)
	if lock {
		q = q.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	err := q.Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = storeError{err, domain.ErrUploadSessionNotFound}
	}
	return UploadSession(row), err
}
func (s *store) UploadParts(ctx context.Context, upload string) ([]domain.UploadPart, error) {
	var rows []model.UploadPart
	err := s.tx.WithContext(ctx).Where("upload_id = ?", upload).Order("part_number ASC").Find(&rows).Error
	result := make([]domain.UploadPart, len(rows))
	for i, r := range rows {
		result[i] = domain.UploadPart(r)
	}
	return result, err
}
func (s *store) RenewUploadActivity(ctx context.Context, upload string, now time.Time) error {
	return s.tx.WithContext(ctx).Model(&model.UploadSession{}).Where("upload_id = ?", upload).Updates(structured.Fields{"last_activity_at": now, "updated_at": now}).Error
}
func activeUploadStatuses() []model.UploadSessionStatus {
	return []model.UploadSessionStatus{model.UploadSessionStatusInitiated, model.UploadSessionStatusUploading, model.UploadSessionStatusFinalizing}
}
func (s *store) RenewActiveUploadActivity(ctx context.Context, upload, file string, partsOnly bool, now time.Time) (bool, error) {
	statuses := activeUploadStatuses()
	if partsOnly {
		statuses = statuses[:2]
	}
	r := s.tx.WithContext(ctx).Model(&model.UploadSession{}).Where("upload_id = ? AND file_id = ? AND status IN ?", upload, file, statuses).Updates(structured.Fields{"last_activity_at": now, "updated_at": now})
	return r.RowsAffected == 1, r.Error
}
func (s *store) NextUploadIngestSequence(ctx context.Context, upload string) (int64, error) {
	var sequence int64
	err := s.tx.WithContext(ctx).Raw(`UPDATE upload_session SET ingest_sequence = ingest_sequence + 1 WHERE upload_id = ? RETURNING ingest_sequence`, upload).Row().Scan(&sequence)
	if errors.Is(err, sql.ErrNoRows) {
		err = storeError{err, domain.ErrUploadSessionNotFound}
	}
	return sequence, err
}
func (s *store) ClaimUploadFinalizing(ctx context.Context, upload, file string, now time.Time) (bool, error) {
	r := s.tx.WithContext(ctx).Model(&model.UploadSession{}).Where("upload_id = ? AND file_id = ? AND status IN ?", upload, file, activeUploadStatuses()).Updates(structured.Fields{"status": model.UploadSessionStatusFinalizing, "last_activity_at": now, "updated_at": now})
	return r.RowsAffected == 1, r.Error
}
func (s *store) ClaimUploadAborted(ctx context.Context, upload, file string, now time.Time) (bool, error) {
	r := s.tx.WithContext(ctx).Model(&model.UploadSession{}).Where("upload_id = ? AND file_id = ? AND status IN ?", upload, file, []model.UploadSessionStatus{model.UploadSessionStatusInitiated, model.UploadSessionStatusUploading, model.UploadSessionStatusFailed, model.UploadSessionStatusAborted}).Updates(structured.Fields{"status": model.UploadSessionStatusAborted, "last_activity_at": now, "updated_at": now})
	return r.RowsAffected == 1, r.Error
}
func (s *store) DeleteFinalizingUpload(ctx context.Context, upload string) (bool, error) {
	r := s.tx.WithContext(ctx).Where("upload_id = ? AND status = ?", upload, model.UploadSessionStatusFinalizing).Delete(&model.UploadSession{})
	return r.RowsAffected == 1, r.Error
}
func (s *store) DeleteAbortedUpload(ctx context.Context, upload string) (bool, error) {
	r := s.tx.WithContext(ctx).Where("upload_id = ? AND status = ?", upload, model.UploadSessionStatusAborted).Delete(&model.UploadSession{})
	return r.RowsAffected == 1, r.Error
}

func (s *store) UploadedPartBytes(ctx context.Context, upload string) (int64, error) {
	var size int64
	err := s.tx.WithContext(ctx).Model(&model.UploadPart{}).Where("upload_id = ?", upload).Select("COALESCE(SUM(size), 0)").Scan(&size).Error
	return size, err
}
