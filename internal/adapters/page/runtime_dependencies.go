package page

import (
	"context"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

// Runtime adapts shared OG and media lifecycles to Page-owned ports.
type Runtime struct {
	cdnDomain string
	refresher *og.Refresher
}

func NewRuntime(cdnDomain string, refresher *og.Refresher) *Runtime {
	if refresher == nil {
		panic("Page runtime OG refresher is required")
	}
	return &Runtime{cdnDomain: strings.TrimRight(strings.TrimSpace(cdnDomain), "/"), refresher: refresher}
}

func (r *Runtime) RequestCurrentWithDB(
	ctx context.Context,
	tx *gorm.DB,
	entityType managev1.OgEntityType,
	entityID string,
	locale string,
	allLocales bool,
	reason string,
) (string, error) {
	plan, err := r.refresher.RequestCurrentWithDB(ctx, tx, entityType, entityID, locale, allLocales, reason)
	if err != nil || plan == nil {
		return "", err
	}
	return plan.RunID, nil
}

func (r *Runtime) CancelAndReleaseEntityWithDB(
	ctx context.Context,
	tx *gorm.DB,
	entityType managev1.OgEntityType,
	ownerType string,
	ownerID string,
) error {
	if err := og.NewLifecycle(tx, r.cdnDomain).CancelEntityWithDB(ctx, tx, entityType, ownerID); err != nil {
		return err
	}
	return r.ReleasePublicAssetBindings(ctx, tx, ownerType, ownerID, "og")
}

func (*Runtime) LockAttachableFilesForUpdate(ctx context.Context, tx *gorm.DB, fileIDs []string) error {
	return mediaasset.LockAttachableFilesForUpdate(ctx, tx, fileIDs)
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

func (r *Runtime) ReleaseExactPublicAssetBindings(
	ctx context.Context,
	tx *gorm.DB,
	ownerType string,
	ownerID string,
	bindingKeys []string,
) error {
	return mediaasset.NewLifecycle(tx, r.cdnDomain).
		ReleaseExactPublicAssetBindings(ctx, ownerType, ownerID, bindingKeys)
}

func (r *Runtime) ResolveReadyAssetRefs(
	ctx context.Context,
	tx *gorm.DB,
	assetIDs []string,
) (map[string]*commonv1.AssetRef, error) {
	refs := make(map[string]*commonv1.AssetRef, len(assetIDs))
	if len(assetIDs) == 0 {
		return refs, nil
	}
	var assets []model.PublicAsset
	if err := tx.WithContext(ctx).
		Where("id IN ? AND status = ?", assetIDs, model.PublicAssetStatusReady).
		Find(&assets).Error; err != nil {
		return nil, err
	}
	lifecycle := mediaasset.NewLifecycle(tx, r.cdnDomain)
	for _, asset := range assets {
		if asset.FileSize == nil || len(asset.SHA256) != 32 {
			continue
		}
		ref, err := lifecycle.AssetRef(asset)
		if err != nil {
			return nil, err
		}
		refs[asset.ID] = ref
	}
	return refs, nil
}

func (*Runtime) LoadUnavailableVersionAttachmentKinds(
	ctx context.Context,
	tx *gorm.DB,
	document proto.Message,
) (map[uuid.UUID]contentv1.MissingAttachmentMediaKind, error) {
	return mediaasset.LoadUnavailableVersionAttachmentKinds(ctx, tx, document)
}
