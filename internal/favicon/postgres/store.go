// Package faviconpostgres is the PostgreSQL store of favicon public assets
// and the reads and deletions File media makes in its own transactions.
package faviconpostgres

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/favicon"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// NewUnits returns the favicon unit of work over db.
func NewUnits(db *gorm.DB, cdnDomain string) mutation.UnitOfWork[favicon.Store] {
	return mutation.Postgres(db, func(tx *gorm.DB) favicon.Store { return Bind(tx, cdnDomain) })
}

// Bind binds the favicon store to db, the caller's transaction or pool.
func Bind(db *gorm.DB, cdnDomain string) favicon.Store {
	return store{db: db, cdnDomain: cdnDomain}
}

// LoadSet returns the generated favicon set of a source File in db.
func LoadSet(ctx context.Context, db *gorm.DB, cdnDomain, sourceFileID string) (*commonv1.FaviconAssetSet, error) {
	return favicon.LoadSet(ctx, Bind(db, cdnDomain), sourceFileID)
}

// Projection returns the legacy favicon and generated set of a source File
// in db.
func Projection(ctx context.Context, db *gorm.DB, cdnDomain, sourceFileID string) (*commonv1.AssetRef, *commonv1.FaviconAssetSet) {
	return favicon.Projection(ctx, Bind(db, cdnDomain), sourceFileID)
}

// RequestDeletion schedules the favicon assets of a source File for deletion
// in tx, the transaction that deletes the File.
func RequestDeletion(ctx context.Context, tx *gorm.DB, sourceFileID string) error {
	return favicon.RequestDeletion(ctx, Bind(tx, ""), sourceFileID)
}

type store struct {
	db        *gorm.DB
	cdnDomain string
}

type derivativeAssetRow struct {
	Type             string  `gorm:"column:type"`
	ID               string  `gorm:"column:id"`
	SourceFileID     *string `gorm:"column:source_file_id"`
	Kind             string  `gorm:"column:kind"`
	ObjectKey        string  `gorm:"column:object_key"`
	Extension        string  `gorm:"column:extension"`
	MimeType         string  `gorm:"column:mime_type"`
	FileSize         *int64  `gorm:"column:file_size"`
	SHA256           []byte  `gorm:"column:sha256"`
	Disposition      string  `gorm:"column:disposition"`
	DownloadFilename *string `gorm:"column:download_filename"`
	Status           string  `gorm:"column:status"`
}

func (s store) SourceMimeType(ctx context.Context, fileID string) (string, bool, error) {
	var source model.File
	if err := s.db.WithContext(ctx).Select("id", "mime_type").Where("id = ?", fileID).Take(&source).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	return source.MimeType, true, nil
}

func (s store) FaviconDerivatives(ctx context.Context, fileID string) ([]favicon.DerivativeAsset, error) {
	var rows []derivativeAssetRow
	if err := s.db.WithContext(ctx).Table("file_derivative AS fd").Select(`
			fd.type, pa.id, pa.source_file_id, pa.kind, pa.object_key, pa.extension, pa.mime_type,
			pa.file_size, pa.sha256, pa.disposition, pa.download_filename, pa.status`).
		Joins("LEFT JOIN public_asset AS pa ON pa.id = fd.asset_id").
		Where("fd.file_id = ? AND fd.type LIKE ?", fileID, "FILE_DERIVATIVE_TYPE_FAVICON_%").
		Order("fd.type ASC").Scan(&rows).Error; err != nil {
		return nil, err
	}
	assets := make([]favicon.DerivativeAsset, len(rows))
	for i, row := range rows {
		assets[i] = favicon.DerivativeAsset{
			Type: row.Type, ID: row.ID, SourceFileID: row.SourceFileID, Kind: row.Kind, ObjectKey: row.ObjectKey,
			Extension: row.Extension, MimeType: row.MimeType, FileSize: row.FileSize, SHA256: row.SHA256,
			Disposition: row.Disposition, DownloadFilename: row.DownloadFilename, Status: row.Status,
		}
	}
	return assets, nil
}

func (s store) NewestReadySourceAsset(ctx context.Context, fileID string) (favicon.DerivativeAsset, bool, error) {
	var asset model.PublicAsset
	if err := s.db.WithContext(ctx).
		Where("source_file_id = ? AND kind = ? AND status = ?", fileID, "favicon", model.PublicAssetStatusReady).
		Order("created_at DESC").
		Take(&asset).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return favicon.DerivativeAsset{}, false, nil
		}
		return favicon.DerivativeAsset{}, false, err
	}
	return assetFromRow(asset), true, nil
}

func (s store) AssetRef(asset favicon.DerivativeAsset) (*commonv1.AssetRef, error) {
	return mediaasset.NewLifecycle(s.db, s.cdnDomain).AssetRef(model.PublicAsset{
		ID:               asset.ID,
		SourceFileID:     asset.SourceFileID,
		Kind:             asset.Kind,
		ObjectKey:        asset.ObjectKey,
		Extension:        asset.Extension,
		MimeType:         asset.MimeType,
		FileSize:         asset.FileSize,
		SHA256:           asset.SHA256,
		Disposition:      asset.Disposition,
		DownloadFilename: asset.DownloadFilename,
		Status:           asset.Status,
	})
}

func (s store) FaviconDerivativeCount(ctx context.Context, fileID string) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Table("file_derivative").
		Where("file_id = ? AND type LIKE ? AND asset_id IS NOT NULL", fileID, "FILE_DERIVATIVE_TYPE_FAVICON_%").
		Count(&count).Error
	return count, err
}

// A schema without public_asset.source_file_id (an in-memory test schema)
// keeps no source assets.
func (s store) tracksSourceAssets() bool {
	return s.db.Migrator().HasColumn(&model.PublicAsset{}, "SourceFileID")
}

func (s store) SourceAssetCount(ctx context.Context, fileID string) (int64, error) {
	if !s.tracksSourceAssets() {
		return 0, nil
	}
	var count int64
	err := s.db.WithContext(ctx).Model(&model.PublicAsset{}).
		Where("source_file_id = ? AND kind = ?", fileID, "favicon").Count(&count).Error
	return count, err
}

func (s store) LockDeletionAssets(ctx context.Context, fileID string) ([]favicon.DerivativeAsset, error) {
	derivativeAssetIDs := s.db.Table("file_derivative").Select("asset_id").
		Where("file_id = ? AND type LIKE ? AND asset_id IS NOT NULL", fileID, "FILE_DERIVATIVE_TYPE_FAVICON_%")
	query := s.db.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Where("id IN (?)", derivativeAssetIDs)
	if s.tracksSourceAssets() {
		query = query.Or("source_file_id = ? AND kind = ?", fileID, "favicon")
	}
	var rows []model.PublicAsset
	if err := query.Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	assets := make([]favicon.DerivativeAsset, len(rows))
	for i, row := range rows {
		assets[i] = assetFromRow(row)
	}
	return assets, nil
}

func (s store) BindingCount(ctx context.Context, assetIDs []string) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&model.PublicAssetBinding{}).Where("asset_id IN ?", assetIDs).Count(&count).Error
	return count, err
}

func (s store) RequestAssetDeletion(ctx context.Context, assetID string) error {
	return mediaasset.NewLifecycle(s.db, "").RequestPublicAssetDeletion(ctx, assetID)
}

func (s store) DanglingReadyAssetIDs(ctx context.Context, cutoff time.Time) ([]string, error) {
	var assetIDs []string
	err := s.db.WithContext(ctx).Model(&model.PublicAsset{}).
		Where("kind = ? AND status = ? AND created_at < ?", "favicon", model.PublicAssetStatusReady, cutoff).
		Where("NOT EXISTS (SELECT 1 FROM public_asset_binding pab WHERE pab.asset_id = public_asset.id)").
		Order("created_at ASC, id ASC").Pluck("id", &assetIDs).Error
	return assetIDs, err
}

func (s store) LockAsset(ctx context.Context, assetID string) (favicon.DerivativeAsset, bool, error) {
	var asset model.PublicAsset
	if err := s.db.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id", "kind", "status", "created_at").Where("id = ?", assetID).Take(&asset).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return favicon.DerivativeAsset{}, false, nil
		}
		return favicon.DerivativeAsset{}, false, err
	}
	return assetFromRow(asset), true, nil
}

func (s store) AssetBound(ctx context.Context, assetID string) (bool, error) {
	var count int64
	if err := s.db.WithContext(ctx).Model(&model.PublicAssetBinding{}).Where("asset_id = ?", assetID).Count(&count).Error; err != nil {
		return false, err
	}
	return count != 0, nil
}

func (s store) MarkAssetDeletePending(ctx context.Context, assetID string, now time.Time) (bool, error) {
	result := s.db.WithContext(ctx).Model(&model.PublicAsset{}).
		Where("id = ? AND status = ?", assetID, model.PublicAssetStatusReady).
		Updates(structured.Fields{"status": model.PublicAssetStatusDeletePending, "delete_requested_at": now, "updated_at": now})
	return result.RowsAffected == 1, result.Error
}

func assetFromRow(asset model.PublicAsset) favicon.DerivativeAsset {
	return favicon.DerivativeAsset{
		ID: asset.ID, SourceFileID: asset.SourceFileID, Kind: asset.Kind, ObjectKey: asset.ObjectKey,
		Extension: asset.Extension, MimeType: asset.MimeType, FileSize: asset.FileSize, SHA256: asset.SHA256,
		Disposition: asset.Disposition, DownloadFilename: asset.DownloadFilename, Status: asset.Status,
		CreatedAt: asset.CreatedAt,
	}
}
