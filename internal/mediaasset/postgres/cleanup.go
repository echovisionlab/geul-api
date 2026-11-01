package mediaassetpostgres

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

func NewCleanupUnits(db *gorm.DB) mutation.UnitOfWork[mediaasset.CleanupStore] {
	if db == nil {
		panic("media asset cleanup units: db is required")
	}
	return mutation.Postgres(db, func(tx *gorm.DB) mediaasset.CleanupStore { return &store{tx: tx} })
}
func (s *store) DueRetiredGenerationIDs(ctx context.Context, now time.Time) ([]string, error) {
	var ids []string
	err := s.tx.WithContext(ctx).Model(&model.MediaGeneration{}).Where("status = ? AND delete_after IS NOT NULL AND delete_after <= ?", model.MediaGenerationStatusRetired, now).Order("delete_after ASC, id ASC").Limit(mediaasset.RetiredGenerationCleanupBatchSize).Pluck("id", &ids).Error
	return ids, err
}
func (s *store) FileDeletionPending(ctx context.Context, id string) (bool, error) {
	var count int64
	err := s.tx.WithContext(ctx).Model(&model.File{}).Where("id = ? AND delete_requested_at IS NOT NULL", id).Count(&count).Error
	return count != 0, err
}
func (s *store) GenerationReferenced(ctx context.Context, id string) (bool, error) {
	var count int64
	err := s.tx.WithContext(ctx).Model(&model.FileDerivative{}).Where("media_generation_id = ?", id).Count(&count).Error
	return count != 0, err
}
func (s *store) DeleteGeneration(ctx context.Context, id string) (bool, error) {
	result := s.tx.WithContext(ctx).Where("id = ?", id).Delete(&model.MediaGeneration{})
	return result.RowsAffected == 1, result.Error
}
func (s *store) ReferencedGenerationIDs(ctx context.Context, ids []string) (map[string]struct{}, error) {
	var values []string
	if err := s.tx.WithContext(ctx).Model(&model.FileDerivative{}).Where("type = ? AND media_generation_id IN ?", managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String(), ids).Pluck("media_generation_id", &values).Error; err != nil {
		return nil, err
	}
	result := make(map[string]struct{}, len(values))
	for _, id := range values {
		result[id] = struct{}{}
	}
	return result, nil
}
func (s *store) PendingAssets(ctx context.Context) ([]mediaasset.Asset, error) {
	var rows []model.PublicAsset
	if err := s.tx.WithContext(ctx).Where("status = ?", model.PublicAssetStatusDeletePending).Order("delete_requested_at ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]mediaasset.Asset, len(rows))
	for i, row := range rows {
		result[i] = Asset(row)
	}
	return result, nil
}
func (s *store) AssetBindingCount(ctx context.Context, id string) (int64, error) {
	var count int64
	err := s.tx.WithContext(ctx).Model(&model.PublicAssetBinding{}).Where("asset_id = ?", id).Count(&count).Error
	return count, err
}
func (s *store) MarkAssetDeleted(ctx context.Context, id string, now time.Time) (bool, error) {
	result := s.tx.WithContext(ctx).Model(&model.PublicAsset{}).Where("id = ? AND status = ?", id, model.PublicAssetStatusDeletePending).Updates(structured.Fields{"status": model.PublicAssetStatusDeleted, "deleted_at": now, "updated_at": now})
	return result.RowsAffected == 1, result.Error
}
func (s *store) ExpiredUnreadyAssetIDs(ctx context.Context, cutoff time.Time) ([]string, error) {
	var ids []string
	err := s.tx.WithContext(ctx).Model(&model.PublicAsset{}).Where("status IN ? AND created_at < ?", []string{model.PublicAssetStatusAllocated, model.PublicAssetStatusFailed}, cutoff).Where("NOT EXISTS (SELECT 1 FROM og_generation WHERE og_generation.id = public_asset.id)").Order("created_at ASC, id ASC").Pluck("id", &ids).Error
	return ids, err
}
func (s *store) AssetHasGeneration(ctx context.Context, id string) (bool, error) {
	var count int64
	err := s.tx.WithContext(ctx).Model(&model.OgGeneration{}).Where("id = ?", id).Count(&count).Error
	return count != 0, err
}
func (s *store) DeleteExpiredAsset(ctx context.Context, id string, cutoff time.Time) (bool, error) {
	result := s.tx.WithContext(ctx).Where("id = ? AND status IN ? AND created_at < ?", id, []string{model.PublicAssetStatusAllocated, model.PublicAssetStatusFailed}, cutoff).Delete(&model.PublicAsset{})
	return result.RowsAffected == 1, result.Error
}
