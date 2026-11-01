package filemediapostgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/model"
	"gorm.io/gorm"
)

func independentUploadType(kind string) bool {
	switch kind {
	case "UPLOAD_TYPE_GENERAL_FILE", "UPLOAD_TYPE_EDITOR_IMAGE", "UPLOAD_TYPE_EDITOR_AUDIO", "UPLOAD_TYPE_EDITOR_VIDEO", "UPLOAD_TYPE_EDITOR_ATTACHMENT", "UPLOAD_TYPE_EDITOR_MESH":
		return true
	}
	return false
}
func activeUploadQuery(q *gorm.DB, f domain.UploadSearch) *gorm.DB {
	return q.Where("(status = ? OR (status IN ? AND last_activity_at >= ? AND chunk_size = ?))", model.UploadSessionStatusFinalizing, []model.UploadSessionStatus{model.UploadSessionStatusInitiated, model.UploadSessionStatusUploading}, f.ActiveAfter, f.ChunkSize)
}
func (s *store) ActiveSurfaceUploads(ctx context.Context, f domain.UploadSearch) ([]domain.UploadSession, error) {
	if independentUploadType(f.UploadType) || f.Mode == domain.UploadTargetUnknown || (f.Mode == domain.UploadTargetGeneral && f.SlotID == "") {
		return nil, nil
	}
	q := s.tx.WithContext(ctx).Model(&model.UploadSession{}).Where("upload_type = ? AND entity_id = ?", f.UploadType, f.EntityID)
	if f.EntityType != nil {
		q = q.Where("entity_type = ?", *f.EntityType)
	}
	if f.Mode == domain.UploadTargetGeneral {
		q = q.Where("slot_id = ?", f.SlotID)
	} else {
		q = q.Where("slot_id IS NULL")
	}
	q = activeUploadQuery(q, f)
	if f.ExpectedCurrentFileID == nil {
		q = q.Where("expected_current_file_id IS NULL")
	} else {
		q = q.Where("expected_current_file_id = ?", *f.ExpectedCurrentFileID)
	}
	var rows []model.UploadSession
	err := q.Order("CASE WHEN status = 'finalizing' THEN 0 ELSE 1 END").Order("last_activity_at DESC").Find(&rows).Error
	result := make([]domain.UploadSession, len(rows))
	for i, r := range rows {
		result[i] = UploadSession(r)
	}
	return result, err
}
func (s *store) FindUploadCandidate(ctx context.Context, f domain.UploadSearch) (domain.UploadSession, bool, error) {
	q := s.tx.WithContext(ctx).Model(&model.UploadSession{}).Where("upload_type = ?", f.UploadType)
	q = activeUploadQuery(q, f)
	if independentUploadType(f.UploadType) {
		q = q.Where("entity_id IS NULL")
	} else {
		q = q.Where("entity_id = ?", f.EntityID)
	}
	if f.EntityType != nil {
		q = q.Where("entity_type = ?", *f.EntityType)
	}
	switch f.Mode {
	case domain.UploadTargetEditorFile, domain.UploadTargetTrackProjection:
		q = q.Where("slot_id IS NULL")
	case domain.UploadTargetGeneral:
		if f.UploadType == "UPLOAD_TYPE_GENERAL_FILE" {
			q = q.Where("slot_id IS NULL")
		} else if f.SlotID != "" {
			q = q.Where("slot_id = ?", f.SlotID)
		}
	default:
		return domain.UploadSession{}, false, fmt.Errorf("upload target classification is missing")
	}
	if f.ExpectedCurrentFileID == nil {
		q = q.Where("expected_current_file_id IS NULL")
	} else {
		q = q.Where("expected_current_file_id = ?", *f.ExpectedCurrentFileID)
	}
	if f.FileID != "" {
		q = q.Where("file_id = ?", f.FileID)
	}
	if f.UploadID != "" {
		q = q.Where("upload_id = ?", f.UploadID)
	}
	if f.FileName != "" {
		q = q.Where("file_name = ?", f.FileName)
	}
	if f.FileSize != nil && *f.FileSize > 0 {
		q = q.Where("file_size = ?", *f.FileSize)
	}
	if f.MimeType != "" {
		q = q.Where("requested_mime = ?", f.MimeType)
	}
	if f.FileLastModified != nil {
		q = q.Where("file_last_modified = ?", *f.FileLastModified)
	}
	var row model.UploadSession
	err := q.Order("CASE WHEN status = 'finalizing' THEN 0 ELSE 1 END").Order("last_activity_at DESC").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.UploadSession{}, false, nil
	}
	return UploadSession(row), err == nil, err
}
