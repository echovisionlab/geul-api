package filemediapostgres

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *store) PromotionFile(ctx context.Context, id string, lock bool) (domain.File, error) {
	var file model.File
	columns := []string{"id", "delete_requested_at"}
	if lock {
		if err := mediaassetpostgres.Bind(s.tx).LockAttachableFiles(ctx, []string{id}); err != nil {
			return domain.File{}, err
		}
		columns = []string{"id", "mime_type", "file_size", "extension"}
	}
	err := s.tx.WithContext(ctx).Select(columns).Where("id = ?", id).Take(&file).Error
	return domain.File(file), fileError(err)
}
func (s *store) SourcePromotionAsset(ctx context.Context, id string, lock bool) (mediaasset.Asset, error) {
	var row model.PublicAsset
	q := s.tx.WithContext(ctx).Where("source_file_id = ? AND status <> ?", id, model.PublicAssetStatusDeleted).Order("created_at DESC")
	if lock {
		q = q.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	err := q.Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = storeError{err, mediaasset.ErrAssetNotFound}
	}
	return mediaassetpostgres.Asset(row), err
}
func (s *store) PromotionAsset(ctx context.Context, id string, lock bool) (mediaasset.Asset, error) {
	if lock {
		return mediaassetpostgres.Bind(s.tx).LockAsset(ctx, id)
	}
	return mediaassetpostgres.Bind(s.tx).Asset(ctx, id)
}
func (s *store) ResumePromotionAsset(ctx context.Context, id string, clearDeletion bool, now time.Time) (bool, error) {
	fields := structured.Fields{"file_size": nil, "sha256": nil, "status": model.PublicAssetStatusAllocated, "ready_at": nil, "failed_at": nil, "failure_reason": nil, "updated_at": now}
	if clearDeletion {
		fields["delete_requested_at"] = nil
		fields["deleted_at"] = nil
	}
	result := s.tx.WithContext(ctx).Model(&model.PublicAsset{}).Where("id = ? AND status = ?", id, model.PublicAssetStatusFailed).Updates(fields)
	return result.RowsAffected == 1, result.Error
}
func (s *store) FailPromotionAsset(ctx context.Context, id, reason string, now time.Time) (bool, error) {
	result := s.tx.WithContext(ctx).Model(&model.PublicAsset{}).Where("id = ? AND status = ?", id, model.PublicAssetStatusAllocated).Updates(structured.Fields{"status": model.PublicAssetStatusFailed, "failed_at": now, "failure_reason": reason, "updated_at": now})
	return result.RowsAffected == 1, result.Error
}
func (s *store) AllocatePromotionAsset(ctx context.Context, cdn string, input mediaasset.Allocation) (*mediaasset.Asset, *mediaasset.WriteTarget, error) {
	return mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(s.tx), cdn).AllocatePublicAsset(ctx, input)
}
func (s *store) CompletePromotionAsset(ctx context.Context, cdn string, input mediaasset.WrittenAsset) error {
	_, err := mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(s.tx), cdn).CompletePublicAsset(ctx, &input)
	return err
}
func (s *store) ReadyPromotionReference(ctx context.Context, cdn, id string) (*mediaasset.Reference, error) {
	return mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(s.tx), cdn).ReadyAssetRef(ctx, id)
}

func (s *store) ReadyPromotionReferenceForFile(ctx context.Context, cdn, file, kind string) (*mediaasset.Reference, error) {
	return mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(s.tx), cdn).ReadyAssetRefForSourceFile(ctx, file, kind)
}
