// Package mediaassetpostgres implements the transaction-bound media asset Store.
package mediaassetpostgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/sliceutil"
	"github.com/echovisionlab/geul-api/internal/structured"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func NewUnits(db *gorm.DB) mutation.UnitOfWork[mediaasset.Store] {
	if db == nil {
		panic("media asset units: db is required")
	}
	return mutation.Postgres(db, func(tx *gorm.DB) mediaasset.Store { return NewStore(tx) })
}

func NewStore(tx *gorm.DB) mediaasset.Store { return &store{tx: tx} }

type store struct{ tx *gorm.DB }

func (s *store) InsertAsset(ctx context.Context, asset mediaasset.Asset) error {
	row := AssetRow(asset)
	return s.tx.WithContext(ctx).Create(&row).Error
}

func (s *store) EnsureAssetAllocation(ctx context.Context, asset mediaasset.Asset) (mediaasset.Asset, error) {
	row := AssetRow(asset)
	if err := s.tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return mediaasset.Asset{}, err
	}
	return s.Asset(ctx, asset.ID)
}

func (s *store) Asset(ctx context.Context, id string) (mediaasset.Asset, error) {
	return s.asset(ctx, id, false)
}
func (s *store) LockAsset(ctx context.Context, id string) (mediaasset.Asset, error) {
	return s.asset(ctx, id, true)
}
func (s *store) asset(ctx context.Context, id string, lock bool, fields ...string) (mediaasset.Asset, error) {
	var row model.PublicAsset
	q := s.tx.WithContext(ctx).Where("id = ?", id)
	if len(fields) > 0 {
		q = q.Select(fields)
	}
	if lock {
		q = q.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	err := q.Take(&row).Error
	return Asset(row), assetError(err)
}

func (s *store) LockAssets(ctx context.Context, ids []string) (map[string]mediaasset.Asset, error) {
	rows, err := lockPublicAssetsForUpdate(ctx, s.tx, ids)
	if err != nil {
		return nil, err
	}
	result := make(map[string]mediaasset.Asset, len(rows))
	for id, row := range rows {
		result[id] = Asset(row)
	}
	return result, nil
}

func (s *store) ReadyAssetForFile(ctx context.Context, id, kind string) (mediaasset.Asset, error) {
	var row model.PublicAsset
	err := s.tx.WithContext(ctx).Where("source_file_id = ? AND kind = ? AND status = ?", strings.TrimSpace(id), kind, model.PublicAssetStatusReady).Order("created_at DESC").Take(&row).Error
	return Asset(row), assetError(err)
}

func (s *store) ReadyAssetsForFiles(ctx context.Context, kind string, ids []string) ([]mediaasset.Asset, error) {
	var rows []model.PublicAsset
	if err := s.tx.WithContext(ctx).Where("source_file_id IN ? AND kind = ? AND status = ?", ids, kind, model.PublicAssetStatusReady).Order("source_file_id ASC, created_at DESC, id DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]mediaasset.Asset, len(rows))
	for i, row := range rows {
		result[i] = Asset(row)
	}
	return result, nil
}

func (s *store) CompleteAsset(ctx context.Context, id string, size int64, digest []byte, now time.Time) error {
	return s.tx.WithContext(ctx).Model(&model.PublicAsset{}).Where("id = ? AND status = ?", id, model.PublicAssetStatusAllocated).Updates(structured.Fields{
		"file_size": size, "sha256": append([]byte(nil), digest...), "status": model.PublicAssetStatusReady, "ready_at": now, "failure_reason": nil, "failed_at": nil, "updated_at": now,
	}).Error
}

func (s *store) Binding(ctx context.Context, ownerType, ownerID, key string) (mediaasset.AssetBinding, bool, error) {
	return s.binding(ctx, ownerType, ownerID, key, false)
}
func (s *store) LockBinding(ctx context.Context, ownerType, ownerID, key string) (mediaasset.AssetBinding, bool, error) {
	return s.binding(ctx, ownerType, ownerID, key, true)
}
func (s *store) binding(ctx context.Context, ownerType, ownerID, key string, lock bool) (mediaasset.AssetBinding, bool, error) {
	row, found, err := loadPublicAssetBinding(ctx, s.tx, ownerType, ownerID, key, lock)
	return assetBinding(row), found, err
}
func (s *store) InsertBinding(ctx context.Context, binding mediaasset.AssetBinding) error {
	row := assetBindingRow(binding)
	err := s.tx.WithContext(ctx).Create(&row).Error
	if dberrors.IsUniqueViolation(err) {
		return classifiedError{err, mediaasset.ErrBindingConflict}
	}
	return err
}
func (s *store) ReplaceBinding(ctx context.Context, binding mediaasset.AssetBinding, expected string) (bool, error) {
	result := s.tx.WithContext(ctx).Model(&model.PublicAssetBinding{}).Where("owner_type = ? AND owner_id = ? AND binding_key = ? AND asset_id = ?", binding.OwnerType, binding.OwnerID, binding.BindingKey, expected).Updates(structured.Fields{"asset_id": binding.AssetID, "source_file_id": binding.SourceFileID, "updated_at": binding.UpdatedAt})
	return result.RowsAffected == 1, result.Error
}
func (s *store) BindingKeys(ctx context.Context, ownerType, ownerID, prefix string) ([]string, error) {
	var keys []string
	err := WhereBindingKeyFamily(s.tx.WithContext(ctx).Model(&model.PublicAssetBinding{}).Where("owner_type = ? AND owner_id = ?", ownerType, ownerID), prefix).Order("binding_key ASC").Pluck("binding_key", &keys).Error
	return keys, err
}
func (s *store) Bindings(ctx context.Context, ownerType, ownerID string, keys []string) ([]mediaasset.AssetBinding, error) {
	return s.bindings(ctx, ownerType, ownerID, keys, false)
}
func (s *store) LockBindings(ctx context.Context, ownerType, ownerID string, keys []string) ([]mediaasset.AssetBinding, error) {
	return s.bindings(ctx, ownerType, ownerID, keys, true)
}
func (s *store) bindings(ctx context.Context, ownerType, ownerID string, keys []string, lock bool) ([]mediaasset.AssetBinding, error) {
	var rows []model.PublicAssetBinding
	q := s.tx.WithContext(ctx).Where("owner_type = ? AND owner_id = ? AND binding_key IN ?", ownerType, ownerID, keys).Order("binding_key ASC")
	if lock {
		q = q.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]mediaasset.AssetBinding, len(rows))
	for i, row := range rows {
		result[i] = assetBinding(row)
	}
	return result, nil
}
func (s *store) LockAssetBindings(ctx context.Context, ids []string) ([]mediaasset.AssetBinding, error) {
	rows, err := lockPublicAssetBindingsForAssets(ctx, s.tx, ids)
	if err != nil {
		return nil, err
	}
	result := make([]mediaasset.AssetBinding, len(rows))
	for i, row := range rows {
		result[i] = assetBinding(row)
	}
	return result, nil
}
func (s *store) DeleteBindings(ctx context.Context, ownerType, ownerID string, keys []string) (int64, error) {
	result := s.tx.WithContext(ctx).Where("owner_type = ? AND owner_id = ? AND binding_key IN ?", ownerType, ownerID, keys).Delete(&model.PublicAssetBinding{})
	return result.RowsAffected, result.Error
}
func (s *store) MarkUnboundAssetsDeletePending(ctx context.Context, ids []string, now time.Time) error {
	return transitionUnboundPublicAssetsToDeletePending(ctx, s.tx, ids, now)
}
func (s *store) MarkAssetDeletePending(ctx context.Context, id string, now time.Time) (bool, error) {
	result := s.tx.WithContext(ctx).Model(&model.PublicAsset{}).Where("id = ? AND status IN ?", id, []string{model.PublicAssetStatusReady, model.PublicAssetStatusFailed}).Updates(structured.Fields{"status": model.PublicAssetStatusDeletePending, "delete_requested_at": now, "updated_at": now})
	return result.RowsAffected == 1, result.Error
}
func (s *store) Generation(ctx context.Context, id string) (mediaasset.Generation, error) {
	return s.generation(ctx, id, false)
}
func (s *store) LockGeneration(ctx context.Context, id string) (mediaasset.Generation, error) {
	return s.generation(ctx, id, true)
}
func (s *store) generation(ctx context.Context, id string, lock bool, fields ...string) (mediaasset.Generation, error) {
	var row model.MediaGeneration
	q := s.tx.WithContext(ctx).Where("id = ?", id)
	if len(fields) > 0 {
		q = q.Select(fields)
	}
	if lock {
		q = q.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	err := q.Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = classifiedError{err, mediaasset.ErrGenerationNotFound}
	}
	return generation(row), err
}
func (s *store) RetireOtherGenerations(ctx context.Context, fileID, id string, now, deleteAfter time.Time) error {
	return s.tx.WithContext(ctx).Model(&model.MediaGeneration{}).Where("file_id = ? AND status = ? AND id <> ?", fileID, model.MediaGenerationStatusReady, id).Updates(structured.Fields{"status": model.MediaGenerationStatusRetired, "retired_at": now, "delete_after": deleteAfter, "updated_at": now}).Error
}
func (s *store) CompleteGeneration(ctx context.Context, id string, digest []byte, count int32, size int64, now time.Time) error {
	return s.tx.WithContext(ctx).Model(&model.MediaGeneration{}).Where("id = ? AND status = ?", id, model.MediaGenerationStatusAllocated).Updates(structured.Fields{"manifest_sha256": append([]byte(nil), digest...), "object_count": count, "total_size": size, "status": model.MediaGenerationStatusReady, "ready_at": now, "updated_at": now}).Error
}

type classifiedError struct {
	error
	kind error
}

func (e classifiedError) Is(target error) bool { return target == e.kind }
func (e classifiedError) Unwrap() error        { return e.error }
func assetError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return classifiedError{err, mediaasset.ErrAssetNotFound}
	}
	return err
}

const bindingKeyFamilyCondition = "binding_key = ? OR substr(binding_key, 1, length(CAST(? AS TEXT))) = ?"

func WhereBindingKeyFamily(db *gorm.DB, prefix string) *gorm.DB {
	return db.Where(bindingKeyFamilyCondition, prefix, prefix+"_", prefix+"_")
}

func loadPublicAssetBinding(
	ctx context.Context,
	tx *gorm.DB,
	ownerType string,
	ownerID string,
	bindingKey string,
	lock bool,
) (model.PublicAssetBinding, bool, error) {
	var binding model.PublicAssetBinding
	query := tx.WithContext(ctx).
		Where("owner_type = ? AND owner_id = ? AND binding_key = ?", ownerType, ownerID, bindingKey)
	if lock {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	result := query.Limit(1).Find(&binding)
	if result.Error != nil {
		return model.PublicAssetBinding{}, false, result.Error
	}
	if result.RowsAffected == 0 {
		return model.PublicAssetBinding{}, false, nil
	}
	return binding, true, nil
}

func lockPublicAssetsForUpdate(
	ctx context.Context,
	tx *gorm.DB,
	assetIDs []string,
) (map[string]model.PublicAsset, error) {
	assetIDs = sliceutil.SortedUniqueTrimmed(assetIDs)
	if len(assetIDs) == 0 {
		return map[string]model.PublicAsset{}, nil
	}
	var assets []model.PublicAsset
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id", "status").
		Where("id IN ?", assetIDs).
		Order("id ASC").
		Find(&assets).Error; err != nil {
		return nil, err
	}
	result := make(map[string]model.PublicAsset, len(assets))
	for _, asset := range assets {
		result[asset.ID] = asset
	}
	return result, nil
}

func lockPublicAssetBindingsForAssets(
	ctx context.Context,
	tx *gorm.DB,
	assetIDs []string,
) ([]model.PublicAssetBinding, error) {
	assetIDs = sliceutil.SortedUniqueTrimmed(assetIDs)
	if len(assetIDs) == 0 {
		return nil, nil
	}
	var bindings []model.PublicAssetBinding
	err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("asset_id").
		Where("asset_id IN ?", assetIDs).
		Order("asset_id ASC").
		Find(&bindings).Error
	return bindings, err
}

func transitionUnboundPublicAssetsToDeletePending(
	ctx context.Context,
	tx *gorm.DB,
	assetIDs []string,
	now time.Time,
) error {
	assetIDs = sliceutil.SortedUniqueTrimmed(assetIDs)
	if len(assetIDs) == 0 {
		return nil
	}
	return tx.WithContext(ctx).Model(&model.PublicAsset{}).
		Where("id IN ? AND status = ?", assetIDs, model.PublicAssetStatusReady).
		Where("source_file_id IS NULL").
		Where("NOT EXISTS (SELECT 1 FROM public_asset_binding AS binding WHERE binding.asset_id = public_asset.id)").
		Updates(structured.Fields{
			"status":              model.PublicAssetStatusDeletePending,
			"delete_requested_at": now,
			"updated_at":          now,
		}).Error
}

func lockAttachableFilesForUpdate(ctx context.Context, tx *gorm.DB, fileIDs []string) error {
	if tx == nil {
		return fmt.Errorf("database transaction is required")
	}
	ids := sliceutil.SortedUniqueTrimmed(fileIDs)
	if len(ids) == 0 {
		return nil
	}
	var files []model.File
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id", "delete_requested_at").
		Where("id IN ?", ids).
		Order("id ASC").
		Find(&files).Error; err != nil {
		return err
	}
	if len(files) != len(ids) {
		return domainerr.Precondition("one or more files no longer exist")
	}
	for _, file := range files {
		if file.DeleteRequestedAt != nil {
			return domainerr.Precondition("file is pending deletion")
		}
	}
	return nil
}

func (s *store) LockAttachableFiles(ctx context.Context, ids []string) error {
	return lockAttachableFilesForUpdate(ctx, s.tx, ids)
}

func Asset(row model.PublicAsset) mediaasset.Asset {
	return mediaasset.Asset{
		ID:                row.ID,
		SourceFileID:      row.SourceFileID,
		Kind:              row.Kind,
		ObjectKey:         row.ObjectKey,
		Extension:         row.Extension,
		MimeType:          row.MimeType,
		FileSize:          row.FileSize,
		SHA256:            row.SHA256,
		Disposition:       row.Disposition,
		DownloadFilename:  row.DownloadFilename,
		Status:            mediaasset.AssetStatus(row.Status),
		ReadyAt:           row.ReadyAt,
		DeleteRequestedAt: row.DeleteRequestedAt,
		DeletedAt:         row.DeletedAt,
		FailedAt:          row.FailedAt,
		FailureReason:     row.FailureReason,
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
	}
}

func AssetRow(value mediaasset.Asset) model.PublicAsset {
	return model.PublicAsset{
		ID:                value.ID,
		SourceFileID:      value.SourceFileID,
		Kind:              value.Kind,
		ObjectKey:         value.ObjectKey,
		Extension:         value.Extension,
		MimeType:          value.MimeType,
		FileSize:          value.FileSize,
		SHA256:            value.SHA256,
		Disposition:       value.Disposition,
		DownloadFilename:  value.DownloadFilename,
		Status:            string(value.Status),
		ReadyAt:           value.ReadyAt,
		DeleteRequestedAt: value.DeleteRequestedAt,
		DeletedAt:         value.DeletedAt,
		FailedAt:          value.FailedAt,
		FailureReason:     value.FailureReason,
		CreatedAt:         value.CreatedAt,
		UpdatedAt:         value.UpdatedAt,
	}
}

func assetBinding(row model.PublicAssetBinding) mediaasset.AssetBinding {
	return mediaasset.AssetBinding{
		AssetID:      row.AssetID,
		OwnerType:    row.OwnerType,
		OwnerID:      row.OwnerID,
		BindingKey:   row.BindingKey,
		SourceFileID: row.SourceFileID,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
}

func assetBindingRow(value mediaasset.AssetBinding) model.PublicAssetBinding {
	return model.PublicAssetBinding{
		AssetID:      value.AssetID,
		OwnerType:    value.OwnerType,
		OwnerID:      value.OwnerID,
		BindingKey:   value.BindingKey,
		SourceFileID: value.SourceFileID,
		CreatedAt:    value.CreatedAt,
		UpdatedAt:    value.UpdatedAt,
	}
}

func generation(row model.MediaGeneration) mediaasset.Generation {
	return mediaasset.Generation{
		ID:             row.ID,
		FileID:         row.FileID,
		Kind:           row.Kind,
		ObjectPrefix:   row.ObjectPrefix,
		ManifestName:   row.ManifestName,
		ManifestSHA256: row.ManifestSHA256,
		ObjectCount:    row.ObjectCount,
		TotalSize:      row.TotalSize,
		Status:         mediaasset.GenerationStatus(row.Status),
		ReadyAt:        row.ReadyAt,
		RetiredAt:      row.RetiredAt,
		DeleteAfter:    row.DeleteAfter,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
	}
}

func (s *store) LockAssetForDeletion(ctx context.Context, id string) (mediaasset.Asset, error) {
	return s.asset(ctx, id, true, "id", "status")
}
func (s *store) LockExpiredAsset(ctx context.Context, id string) (mediaasset.Asset, error) {
	return s.asset(ctx, id, true, "id", "kind", "status", "object_key", "created_at")
}
func (s *store) LockRetiredGeneration(ctx context.Context, id string) (mediaasset.Generation, error) {
	return s.generation(ctx, id, true, "id", "file_id", "status", "object_prefix", "delete_after")
}
