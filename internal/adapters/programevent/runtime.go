package programevent

import (
	"context"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"gorm.io/gorm"
)

// Runtime adapts the shared File/media lifecycle to Program Event-owned ports.
type Runtime struct {
	cdnDomain string
}

func NewRuntime(cdnDomain string) *Runtime {
	return &Runtime{cdnDomain: strings.TrimRight(strings.TrimSpace(cdnDomain), "/")}
}

func (*Runtime) LockAttachableFilesForUpdate(ctx context.Context, tx *gorm.DB, fileIDs []string) error {
	return mediaasset.LockAttachableFilesForUpdate(ctx, tx, fileIDs)
}

func (r *Runtime) BindReadyAssetForSourceFile(
	ctx context.Context,
	tx *gorm.DB,
	sourceFileID string,
	ownerType string,
	ownerID string,
	bindingKey string,
	expectedKind string,
) (*commonv1.AssetRef, error) {
	return mediaasset.NewLifecycle(tx, r.cdnDomain).BindReadyAssetForSourceFile(
		ctx, sourceFileID, ownerType, ownerID, bindingKey, expectedKind,
	)
}

func (r *Runtime) ReleasePublicAssetBindings(
	ctx context.Context,
	tx *gorm.DB,
	ownerType string,
	ownerID string,
	bindingPrefix string,
) error {
	return mediaasset.NewLifecycle(tx, r.cdnDomain).
		ReleasePublicAssetBindings(ctx, ownerType, ownerID, bindingPrefix)
}

func (r *Runtime) ReadyPublicAssetRefForSourceFile(
	ctx context.Context,
	tx *gorm.DB,
	sourceFileID string,
	expectedKind string,
) (*commonv1.AssetRef, error) {
	return mediaasset.ReadyPublicAssetRefForSourceFile(ctx, tx, r.cdnDomain, sourceFileID, expectedKind)
}

func (r *Runtime) ResolveSingleReadyInlineAssetForSourceFile(
	ctx context.Context,
	tx *gorm.DB,
	sourceFileID string,
	expectedKind string,
) (*commonv1.AssetRef, error) {
	var assets []model.PublicAsset
	if err := tx.WithContext(ctx).Raw(`
		SELECT public_asset.* FROM public_asset
		WHERE public_asset.source_file_id = ? AND public_asset.kind = ?
		  AND public_asset.status = ? AND public_asset.disposition = 'inline'
		  AND public_asset.delete_requested_at IS NULL AND public_asset.deleted_at IS NULL
		ORDER BY public_asset.id
	`, sourceFileID, expectedKind, model.PublicAssetStatusReady).Scan(&assets).Error; err != nil {
		return nil, fmt.Errorf("load Program Event File public asset: %w", err)
	}
	if len(assets) != 1 {
		return nil, fmt.Errorf("program event Content Block image File has %d ready public assets", len(assets))
	}
	if assets[0].FileSize == nil || *assets[0].FileSize <= 0 || len(assets[0].SHA256) != 32 {
		return nil, fmt.Errorf("program event Content Block image File public asset integrity metadata is incomplete")
	}
	return mediaasset.NewLifecycle(tx, r.cdnDomain).AssetRef(assets[0])
}
