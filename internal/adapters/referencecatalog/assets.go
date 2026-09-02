package referencecatalogadapter

import (
	"context"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	publicreferencecatalog "github.com/echovisionlab/geul-api/internal/referencecatalog/public"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// Assets adapts file-owned lifecycle operations to reference catalogs.
type Assets struct {
	cdnDomain string
}

var _ referencecatalog.Assets = Assets{}

func NewAssets(cdnDomain string) Assets {
	return Assets{cdnDomain: cdnDomain}
}

func (a Assets) LockForAttachment(ctx context.Context, db *gorm.DB, fileIDs []string) error {
	return mediaasset.LockAttachableFilesForUpdate(ctx, db, fileIDs)
}

func (a Assets) BindReady(
	ctx context.Context,
	db *gorm.DB,
	binding referencecatalog.AssetBinding,
) (*commonv1.AssetRef, error) {
	return mediaasset.NewLifecycle(db, a.cdnDomain).BindReadyAssetForSourceFile(
		ctx,
		binding.SourceFileID,
		binding.Owner.Type,
		binding.Owner.ID,
		binding.Key,
		binding.Kind,
	)
}

func (a Assets) Release(
	ctx context.Context,
	db *gorm.DB,
	release referencecatalog.AssetRelease,
) error {
	return mediaasset.NewLifecycle(db, a.cdnDomain).
		ReleasePublicAssetBindings(ctx, release.Owner.Type, release.Owner.ID, release.BindingPrefix)
}

func (a Assets) ReadyRef(
	ctx context.Context,
	db *gorm.DB,
	source referencecatalog.AssetSource,
) (*commonv1.AssetRef, error) {
	return mediaasset.NewLifecycle(db, a.cdnDomain).
		ReadyAssetRefForSourceFile(ctx, source.FileID, source.Kind)
}

// PublicAssets resolves ready file-owned assets for public catalog projections.
type PublicAssets struct {
	cdnDomain string
}

var _ publicreferencecatalog.AssetReader = PublicAssets{}

func NewPublicAssets(cdnDomain string) PublicAssets {
	return PublicAssets{cdnDomain: cdnDomain}
}

func (a PublicAssets) ReadyRef(
	ctx context.Context,
	db *gorm.DB,
	source referencecatalog.AssetSource,
) *commonv1.AssetRef {
	kinds := make([]string, 0, 1+len(source.FallbackKinds))
	kinds = append(kinds, source.Kind)
	kinds = append(kinds, source.FallbackKinds...)

	var asset model.PublicAsset
	query := db.WithContext(ctx).
		Where("source_file_id = ? AND status = ?", strings.TrimSpace(source.FileID), model.PublicAssetStatusReady)
	if len(kinds) > 0 {
		query = query.Where("kind IN ?", kinds)
	}
	if err := query.Order("created_at DESC, id DESC").Take(&asset).Error; err != nil {
		return nil
	}
	expectedExtension := model.GetExtensionFromMime(asset.MimeType)
	if strings.TrimSpace(asset.ID) == "" || strings.TrimSpace(asset.Extension) == "" || strings.TrimSpace(asset.MimeType) == "" ||
		asset.FileSize == nil || *asset.FileSize <= 0 || len(asset.SHA256) != 32 ||
		expectedExtension == "bin" || expectedExtension != asset.Extension {
		return nil
	}
	ref, err := mediaasset.NewLifecycle(db, a.cdnDomain).AssetRef(asset)
	if err != nil {
		return nil
	}
	return ref
}
