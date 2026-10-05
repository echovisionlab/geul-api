package label

import (
	"context"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// Runtime adapts shared File/media and OG lifecycles to Label-owned ports.
type Runtime struct {
	cdnDomain string
	refresher *og.Refresher
}

func NewRuntime(cdnDomain string, refresher *og.Refresher) *Runtime {
	if refresher == nil {
		panic("Label runtime OG refresher is required")
	}
	return &Runtime{
		cdnDomain: strings.TrimRight(strings.TrimSpace(cdnDomain), "/"),
		refresher: refresher,
	}
}

func (*Runtime) LockAttachableFilesForUpdate(ctx context.Context, tx *gorm.DB, fileIDs []string) error {
	return mediaasset.LockAttachableFilesForUpdate(ctx, tx, fileIDs)
}

func (runtime *Runtime) BindReadyAssetForSourceFile(
	ctx context.Context,
	tx *gorm.DB,
	fileID, ownerType, ownerID, bindingKey, kind string,
) (*commonv1.AssetRef, error) {
	return mediaasset.NewLifecycle(tx, runtime.cdnDomain).
		BindReadyAssetForSourceFile(ctx, fileID, ownerType, ownerID, bindingKey, kind)
}

func (runtime *Runtime) ReleasePublicAssetBindings(
	ctx context.Context,
	tx *gorm.DB,
	ownerType, ownerID, bindingPrefix string,
) error {
	return mediaasset.NewLifecycle(tx, runtime.cdnDomain).
		ReleasePublicAssetBindings(ctx, ownerType, ownerID, bindingPrefix)
}

func (runtime *Runtime) ReleaseExactPublicAssetBindings(
	ctx context.Context,
	tx *gorm.DB,
	ownerType, ownerID string,
	bindingKeys []string,
) error {
	return mediaasset.NewLifecycle(tx, runtime.cdnDomain).
		ReleaseExactPublicAssetBindings(ctx, ownerType, ownerID, bindingKeys)
}

func (runtime *Runtime) ResolveReadyAssetRefs(
	ctx context.Context,
	tx *gorm.DB,
	assetIDs []string,
) (map[string]*commonv1.AssetRef, error) {
	ids := normalizeIDs(assetIDs)
	refs := make(map[string]*commonv1.AssetRef, len(ids))
	if len(ids) == 0 {
		return refs, nil
	}
	var assets []model.PublicAsset
	if err := tx.WithContext(ctx).
		Where("id IN ? AND status = ?", ids, model.PublicAssetStatusReady).
		Find(&assets).Error; err != nil {
		return nil, err
	}
	return runtime.assetRefs(tx, assets, false)
}

func (runtime *Runtime) ResolveReadySourceFileRefs(
	ctx context.Context,
	tx *gorm.DB,
	fileIDs []string,
	kinds ...string,
) (map[string]*commonv1.AssetRef, error) {
	ids := normalizeIDs(fileIDs)
	refs := make(map[string]*commonv1.AssetRef, len(ids))
	if len(ids) == 0 {
		return refs, nil
	}
	query := tx.WithContext(ctx).
		Where("source_file_id IN ? AND status = ?", ids, model.PublicAssetStatusReady)
	if len(kinds) > 0 {
		query = query.Where("kind IN ?", kinds)
	}
	var assets []model.PublicAsset
	if err := query.Order("source_file_id ASC, created_at DESC, id DESC").Find(&assets).Error; err != nil {
		return nil, err
	}
	return runtime.assetRefs(tx, assets, true)
}

func (runtime *Runtime) assetRefs(
	tx *gorm.DB,
	assets []model.PublicAsset,
	bySourceFile bool,
) (map[string]*commonv1.AssetRef, error) {
	refs := make(map[string]*commonv1.AssetRef, len(assets))
	lifecycle := mediaasset.NewLifecycle(tx, runtime.cdnDomain)
	for _, asset := range assets {
		key := asset.ID
		if bySourceFile {
			if asset.SourceFileID == nil {
				continue
			}
			key = *asset.SourceFileID
		}
		if refs[key] != nil || asset.FileSize == nil || len(asset.SHA256) != 32 {
			continue
		}
		ref, err := lifecycle.AssetRef(asset)
		if err != nil {
			return nil, err
		}
		refs[key] = ref
	}
	return refs, nil
}

func (runtime *Runtime) RequestCurrentWithDB(
	ctx context.Context,
	tx *gorm.DB,
	labelID string,
	reason string,
) (string, error) {
	plan, err := runtime.refresher.RequestCurrentWithDB(
		ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_LABEL, labelID, "", false, reason,
	)
	if err != nil || plan == nil {
		return "", err
	}
	return strings.TrimSpace(plan.RunID), nil
}

func (runtime *Runtime) CancelAndReleaseOGWithDB(
	ctx context.Context,
	tx *gorm.DB,
	labelID string,
) error {
	if err := og.NewLifecycle(tx, runtime.cdnDomain).CancelEntityWithDB(
		ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_LABEL, labelID,
	); err != nil {
		return err
	}
	return runtime.ReleasePublicAssetBindings(ctx, tx, "label", labelID, "og")
}

func normalizeIDs(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
