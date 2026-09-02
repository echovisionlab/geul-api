package form

import (
	"context"

	formdomain "github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/og"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"gorm.io/gorm"

	formogadapter "github.com/echovisionlab/geul-api/internal/adapters/form/og"
)

type Assets struct {
	cdnDomain string
}

func NewAssets(cdnDomain string) *Assets {
	return &Assets{cdnDomain: cdnDomain}
}

func (a *Assets) LockAttachableFiles(ctx context.Context, tx *gorm.DB, fileIDs []string) error {
	return mediaasset.LockAttachableFilesForUpdate(ctx, tx, fileIDs)
}

func (a *Assets) BindFeaturedImage(
	ctx context.Context,
	tx *gorm.DB,
	fileID, formID string,
) (*commonv1.AssetRef, error) {
	return mediaasset.NewLifecycle(tx, a.cdnDomain).BindReadyAssetForSourceFile(
		ctx,
		fileID,
		"form",
		formID,
		"featured_image",
		"image",
	)
}

func (a *Assets) ReleaseFeaturedImage(ctx context.Context, tx *gorm.DB, formID string) error {
	return mediaasset.NewLifecycle(tx, a.cdnDomain).ReleasePublicAssetBindings(
		ctx,
		"form",
		formID,
		"featured_image",
	)
}

func (a *Assets) FeaturedImage(ctx context.Context, db *gorm.DB, formID string) *commonv1.AssetRef {
	var row struct {
		FileID *string `gorm:"column:featured_image_file_id"`
	}
	if err := db.WithContext(ctx).Table("form").Select("featured_image_file_id").
		Where("id = ?", formID).Scan(&row).Error; err != nil || row.FileID == nil {
		return nil
	}
	asset, err := mediaasset.ReadyPublicAssetRefForSourceFile(ctx, db, a.cdnDomain, *row.FileID, "image")
	if err != nil {
		return nil
	}
	return asset
}

func (*Assets) LocalizedOGDisposition(
	ctx context.Context,
	db *gorm.DB,
	formID, locale string,
) (formdomain.LocalizedOGDisposition, error) {
	disposition, err := og.ResolveExactLocalizedGeneration(ctx, db, "form", formID, locale)
	if err != nil {
		return formdomain.LocalizedOGUnavailable, err
	}
	switch disposition {
	case og.LocalizedGenerationPending:
		return formdomain.LocalizedOGPending, nil
	case og.LocalizedGenerationReady:
		return formdomain.LocalizedOGReady, nil
	default:
		return formdomain.LocalizedOGUnavailable, nil
	}
}

func (a *Assets) ResolvedOG(
	ctx context.Context,
	db *gorm.DB,
	sourceID, localizedID *string,
) (*commonv1.AssetRef, error) {
	return formogadapter.ReadyAsset(ctx, db, a.cdnDomain, localizedID, sourceID)
}

var _ formdomain.Assets = (*Assets)(nil)
var _ formdomain.PublicAssets = (*Assets)(nil)
