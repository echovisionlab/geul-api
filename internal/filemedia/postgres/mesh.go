package filemediapostgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/sliceutil"
	"github.com/echovisionlab/geul-api/internal/structured"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type MeshJobs interface {
	RequirePublisher() error
	Enqueue(context.Context, *gorm.DB, domain.MeshJob) error
}

func MeshCandidate(row model.MeshOptimizationCandidate) domain.MeshCandidate {
	return domain.MeshCandidate(row)
}
func meshError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return storeError{err, domain.ErrMeshCandidateNotFound}
	}
	return err
}
func (s *store) MeshCandidates(ctx context.Context, source, pipeline string) ([]domain.MeshCandidate, error) {
	var rows []model.MeshOptimizationCandidate
	err := s.tx.WithContext(ctx).Where("source_file_id = ? AND pipeline_version = ?", source, pipeline).Order("target_ratio_percent ASC, created_at DESC").Find(&rows).Error
	result := make([]domain.MeshCandidate, len(rows))
	for i, row := range rows {
		result[i] = MeshCandidate(row)
	}
	return result, err
}
func (s *store) MeshCandidate(ctx context.Context, id, job string, lock bool) (domain.MeshCandidate, error) {
	var row model.MeshOptimizationCandidate
	q := s.tx.WithContext(ctx).Where("id = ?", id)
	if job != "" {
		q = q.Where("job_id = ?", job)
	}
	if lock {
		q = q.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	err := q.Take(&row).Error
	return MeshCandidate(row), meshError(err)
}
func (s *store) MeshCandidateByCacheKey(ctx context.Context, key string) (domain.MeshCandidate, error) {
	var row model.MeshOptimizationCandidate
	err := s.tx.WithContext(ctx).Where("cache_key = ?", key).Take(&row).Error
	return MeshCandidate(row), meshError(err)
}
func (s *store) InsertMeshCandidate(ctx context.Context, input domain.MeshCandidate) error {
	row := model.MeshOptimizationCandidate(input)
	return s.tx.WithContext(ctx).Create(&row).Error
}
func (s *store) RequeueMeshCandidate(ctx context.Context, id string, input domain.MeshRequeue) error {
	fields := structured.Fields{"status": model.MeshOptimizationCandidateStatusPending, "output_object_id": input.OutputObjectID, "output_file_id": nil, "job_id": input.JobID, "error_message": nil, "selected_at": nil, "expires_at": input.ExpiresAt, "enqueued_at": input.At, "processing_started_at": nil, "completed_at": nil, "failed_at": nil, "cancelled_at": nil, "updated_at": input.At}
	if input.EntityType != nil {
		fields["entity_type"] = *input.EntityType
	}
	if input.EntityID != nil {
		fields["entity_id"] = *input.EntityID
	}
	return s.tx.WithContext(ctx).Model(&model.MeshOptimizationCandidate{}).Where("id = ?", id).Updates(fields).Error
}
func (s *store) MarkMeshProcessing(ctx context.Context, id string, at, expires time.Time) error {
	return s.tx.WithContext(ctx).Model(&model.MeshOptimizationCandidate{}).Where("id = ? AND status = ?", id, model.MeshOptimizationCandidateStatusPending).Updates(structured.Fields{"status": model.MeshOptimizationCandidateStatusProcessing, "processing_started_at": at, "expires_at": expires, "updated_at": at}).Error
}
func (s *store) SelectMeshCandidate(ctx context.Context, id string, at time.Time) error {
	return s.tx.WithContext(ctx).Model(&model.MeshOptimizationCandidate{}).Where("id = ?", id).Updates(structured.Fields{"selected_at": at, "expires_at": nil, "updated_at": at}).Error
}
func (s *store) CancelMeshCandidate(ctx context.Context, id string, at, expires time.Time) error {
	return s.tx.WithContext(ctx).Model(&model.MeshOptimizationCandidate{}).Where("id = ?", id).Updates(structured.Fields{"status": model.MeshOptimizationCandidateStatusCancelled, "cancelled_at": at, "expires_at": expires, "updated_at": at}).Error
}
func (s *store) RetireMeshCandidate(ctx context.Context, id string, at time.Time) error {
	return s.tx.WithContext(ctx).Model(&model.MeshOptimizationCandidate{}).Where("id = ?", id).Updates(structured.Fields{"status": model.MeshOptimizationCandidateStatusCancelled, "output_file_id": nil, "cancelled_at": at, "updated_at": at}).Error
}
func (s *store) CompleteMeshCandidate(ctx context.Context, id string, v domain.MeshCompletion) error {
	fields := structured.Fields{"output_file_id": v.OutputFileID, "optimized_file_size": v.OptimizedFileSize, "original_file_size": v.OriginalFileSize, "original_vertexes": v.OriginalVertexes, "optimized_vertexes": v.OptimizedVertexes, "original_triangles": v.OriginalTriangles, "optimized_triangles": v.OptimizedTriangles, "processing_time_ms": v.ProcessingTimeMS, "error_message": nil, "completed_at": v.CompletedAt, "expires_at": v.ExpiresAt, "updated_at": v.CompletedAt, "status": v.Status}
	if v.Status == model.MeshOptimizationCandidateStatusReady {
		fields["failed_at"], fields["cancelled_at"] = nil, nil
	} else {
		fields["cancelled_at"] = v.CompletedAt
	}
	return s.tx.WithContext(ctx).Model(&model.MeshOptimizationCandidate{}).Where("id = ?", id).Updates(fields).Error
}
func (s *store) FailMeshCandidate(ctx context.Context, id, message string, at, expires time.Time) error {
	return s.tx.WithContext(ctx).Model(&model.MeshOptimizationCandidate{}).Where("id = ?", id).Updates(structured.Fields{"status": model.MeshOptimizationCandidateStatusFailed, "error_message": message, "failed_at": at, "expires_at": expires, "updated_at": at}).Error
}
func (s *store) ExpiredMeshCandidates(ctx context.Context, at time.Time) ([]domain.MeshCandidate, error) {
	var rows []model.MeshOptimizationCandidate
	err := s.tx.WithContext(ctx).Where("selected_at IS NULL").Where("expires_at IS NOT NULL AND expires_at < ?", at).Find(&rows).Error
	result := make([]domain.MeshCandidate, len(rows))
	for i, row := range rows {
		result[i] = MeshCandidate(row)
	}
	return result, err
}
func (s *store) DeleteMeshCandidate(ctx context.Context, id string) error {
	return s.tx.WithContext(ctx).Where("id = ?", id).Delete(&model.MeshOptimizationCandidate{}).Error
}
func (s *store) LockMeshCompletionFiles(ctx context.Context, source, output string) (bool, error) {
	var rows []model.File
	err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Select("id", "delete_requested_at").Where("id IN ?", sliceutil.SortedUniqueTrimmed([]string{source, output})).Order("id ASC").Find(&rows).Error
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if row.ID == source {
			return row.DeleteRequestedAt == nil, nil
		}
	}
	return false, storeError{gorm.ErrRecordNotFound, domain.ErrFileNotFound}
}
func (s *store) InsertMeshOutputFile(ctx context.Context, input domain.File) (bool, error) {
	row := model.File(input)
	result := s.tx.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoNothing: true}).Create(&row)
	return result.RowsAffected == 1, result.Error
}
func (s *store) MeshSourceFile(ctx context.Context, id string) (domain.File, error) {
	var row model.File
	err := s.tx.WithContext(ctx).Where("id = ? AND delete_requested_at IS NULL", id).Take(&row).Error
	return domain.File(row), fileError(err)
}
func (s *store) MeshSourceLinked(ctx context.Context, kind, owner, file string) (bool, error) {
	switch kind {
	case "post", "page", "work", "program_event":
	default:
		return false, fmt.Errorf("unsupported mesh owner %s", kind)
	}
	var count int64
	err := s.tx.WithContext(ctx).Table("content_block_attachment AS cbf").Joins("JOIN content_block AS cb ON cb.id = cbf.block_id").Joins("JOIN "+kind+" AS owner ON owner.content_document_id = cb.document_id").Where("owner.id = ? AND cbf.selector_kind = 'active' AND cbf.file_id = ?", owner, file).Count(&count).Error
	return count != 0, err
}
func (s *store) RequireMeshPublisher(context.Context) error {
	if s.ports.MeshJobs == nil {
		return fmt.Errorf("mesh optimization publisher is not configured")
	}
	return s.ports.MeshJobs.RequirePublisher()
}
func (s *store) EnqueueMeshJob(ctx context.Context, job domain.MeshJob) error {
	if err := s.RequireMeshPublisher(ctx); err != nil {
		return err
	}
	return s.ports.MeshJobs.Enqueue(ctx, s.tx, job)
}
