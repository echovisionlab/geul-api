package filemediapostgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainaudit"

	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	faviconpostgres "github.com/echovisionlab/geul-api/internal/favicon/postgres"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *store) LockDeletionCandidate(ctx context.Context, id string) (domain.File, error) {
	var row model.File
	err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Select("id", "delete_requested_at").Where("id = ?", id).First(&row).Error
	return domain.File(row), fileError(err)
}
func (s *store) LockDeletionCandidates(ctx context.Context, ids []string) ([]domain.File, error) {
	var rows []model.File
	err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Select("id", "delete_requested_at").Where("id IN ?", ids).Order("id ASC").Find(&rows).Error
	result := make([]domain.File, len(rows))
	for i, r := range rows {
		result[i] = domain.File(r)
	}
	return result, err
}
func fileError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return storeError{err, domain.ErrFileNotFound}
	}
	return err
}
func (s *store) LockDerivativeDeletionAssets(ctx context.Context, id string) ([]mediaasset.Asset, error) {
	var ids []string
	if err := s.tx.WithContext(ctx).Table("file_derivative").Distinct("asset_id").Where("file_id = ? AND asset_id IS NOT NULL", id).Where("type NOT LIKE ?", "FILE_DERIVATIVE_TYPE_FAVICON_%").Order("asset_id ASC").Pluck("asset_id", &ids).Error; err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	var rows []model.PublicAsset
	if err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Where("id IN ?", ids).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) != len(ids) {
		return nil, domainerr.Precondition("file derivative references a missing public asset")
	}
	return deletionAssets(rows), nil
}
func (s *store) LockSourceDeletionAssets(ctx context.Context, id string) ([]mediaasset.Asset, error) {
	var rows []model.PublicAsset
	if err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Where("source_file_id = ?", id).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return deletionAssets(rows), nil
}
func deletionAssets(rows []model.PublicAsset) []mediaasset.Asset {
	out := make([]mediaasset.Asset, len(rows))
	for i, r := range rows {
		out[i] = mediaassetpostgres.Asset(r)
	}
	return out
}
func (s *store) BoundDeletionAssetIDs(ctx context.Context, ids []string) (map[string]bool, error) {
	var bound []string
	if err := s.tx.WithContext(ctx).Model(&model.PublicAssetBinding{}).Distinct("asset_id").Where("asset_id IN ?", ids).Pluck("asset_id", &bound).Error; err != nil {
		return nil, err
	}
	result := map[string]bool{}
	for _, id := range bound {
		result[id] = true
	}
	return result, nil
}
func (s *store) RequestDeletionAsset(ctx context.Context, id string) error {
	return mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(s.tx), "").RequestPublicAssetDeletion(ctx, id)
}
func (s *store) RequestDeletionFavicon(ctx context.Context, id string) error {
	return faviconpostgres.RequestDeletion(ctx, s.tx, id)
}
func (s *store) LockDeletionMeshCandidates(ctx context.Context, id string) ([]domain.DeletionMeshCandidate, error) {
	var rows []model.MeshOptimizationCandidate
	if err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Where("source_file_id = ? AND selected_at IS NULL", id).Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]domain.DeletionMeshCandidate, len(rows))
	for i, r := range rows {
		result[i] = domain.DeletionMeshCandidate{ID: r.ID, Status: r.Status, OutputFileID: r.OutputFileID}
	}
	return result, nil
}
func (s *store) CancelDeletionMeshCandidate(ctx context.Context, id string, now, expires time.Time) error {
	return s.tx.WithContext(ctx).Model(&model.MeshOptimizationCandidate{}).Where("id = ?", id).Updates(structured.Fields{"status": model.MeshOptimizationCandidateStatusCancelled, "cancelled_at": now, "expires_at": expires, "updated_at": now}).Error
}
func (s *store) DeleteDeletionMeshCandidate(ctx context.Context, id string) error {
	return s.tx.WithContext(ctx).Where("id = ?", id).Delete(&model.MeshOptimizationCandidate{}).Error
}
func (s *store) MarkFileDeletion(ctx context.Context, id string, now time.Time) (bool, error) {
	r := s.tx.WithContext(ctx).Table("file").Where("id = ? AND delete_requested_at IS NULL", id).Update("delete_requested_at", now)
	return r.RowsAffected == 1, r.Error
}
func (s *store) DeletionEventFile(ctx context.Context, id string) (domain.File, error) {
	var row model.File
	err := s.tx.WithContext(ctx).Select("id", "extension", "mime_type", "delete_requested_at").Where("id = ? AND delete_requested_at IS NOT NULL", id).First(&row).Error
	return domain.File(row), fileError(err)
}
func (s *store) DeletionGenerationRefs(ctx context.Context, id string) ([]domain.FileGenerationDeleteRef, error) {
	var rows []domain.FileGenerationDeleteRef
	err := s.tx.WithContext(ctx).Table("media_generation").Select("id", "file_id", "kind", "object_prefix").Where("file_id = ?", id).Order("id ASC").Find(&rows).Error
	return rows, err
}
func (s *store) EnqueueFileDeletion(ctx context.Context, target domain.DeletionTarget) error {
	if s.ports.DeletionQueue == nil {
		return fmt.Errorf("file delete publisher must support transactional enqueue")
	}
	executor, err := contentsignal.TransactionExecutor(s.tx)
	if err != nil {
		return err
	}
	return s.ports.DeletionQueue.EnqueueFileDeletion(ctx, executor, target)
}
func (s *store) DeletionAudit(writer domainaudit.Appender) domainaudit.Appender {
	return domainaudit.Bind(s.tx, writer)
}

func (s *store) LockFolderSubtree(ctx context.Context, id string) ([]string, error) {
	if err := s.LockFolderHierarchy(ctx); err != nil {
		return nil, err
	}
	if _, err := s.LockFolder(ctx, id); err != nil {
		return nil, err
	}
	var ids []string
	if err := s.tx.WithContext(ctx).Raw(`WITH RECURSIVE subtree AS (SELECT id FROM file_folder WHERE id = ? UNION ALL SELECT child.id FROM file_folder child JOIN subtree parent ON child.parent_id = parent.id) SELECT id::text FROM subtree ORDER BY id`, id).Scan(&ids).Error; err != nil {
		return nil, err
	}
	if err := s.tx.WithContext(ctx).Table("file_folder").Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Where("id IN ?", ids).Order("id").Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}
func (s *store) FolderDeletionFileIDs(ctx context.Context, ids []string) ([]string, error) {
	var files []string
	err := s.tx.WithContext(ctx).Table("file").Where("folder_id IN ? AND delete_requested_at IS NULL", ids).Order("id").Pluck("id", &files).Error
	return files, err
}
func (s *store) DetachDeletionFiles(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return s.tx.Table("file").Where("id IN ?", ids).Updates(structured.Fields{"folder_id": nil, "updated_at": gorm.Expr("CURRENT_TIMESTAMP")}).Error
}
func (s *store) DeleteFolder(ctx context.Context, id string) error {
	return s.tx.Delete(&model.FileFolder{}, "id = ?", id).Error
}
