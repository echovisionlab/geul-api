package work

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

// Runtime adapts shared OG and media lifecycles to Work-owned ports.
type Runtime struct {
	db        *gorm.DB
	cdnDomain string
	refresher *og.Refresher
}

func NewRuntime(db *gorm.DB, cdnDomain string, refresher *og.Refresher) *Runtime {
	if db == nil || refresher == nil {
		panic("Work runtime database and OG refresher are required")
	}
	return &Runtime{db: db, cdnDomain: strings.TrimRight(strings.TrimSpace(cdnDomain), "/"), refresher: refresher}
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

func (r *Runtime) ReadyPublicAssetRefForSourceFile(
	ctx context.Context,
	tx *gorm.DB,
	sourceFileID string,
	expectedKind string,
) (*commonv1.AssetRef, error) {
	return mediaasset.ReadyPublicAssetRefForSourceFile(ctx, tx, r.cdnDomain, sourceFileID, expectedKind)
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

func (*Runtime) LoadUnavailableVersionAttachmentKinds(
	ctx context.Context,
	tx *gorm.DB,
	document proto.Message,
) (map[uuid.UUID]contentv1.MissingAttachmentMediaKind, error) {
	return mediaasset.LoadUnavailableVersionAttachmentKinds(ctx, tx, document)
}

func (r *Runtime) ResolveReadyOGAsset(
	ctx context.Context,
	sourceAssetID *string,
	localizedAssetID *string,
) (*commonv1.AssetRef, error) {
	ids := normalizedAssetIDs(localizedAssetID, sourceAssetID)
	refs, err := r.ResolveReadyAssetRefs(ctx, r.db, ids)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if refs[id] != nil {
			return refs[id], nil
		}
	}
	return nil, nil
}

func (r *Runtime) IsReadyAsset(ctx context.Context, db *gorm.DB, assetID string) (bool, error) {
	refs, err := r.ResolveReadyAssetRefs(ctx, db, []string{strings.TrimSpace(assetID)})
	if err != nil {
		return false, err
	}
	return refs[strings.TrimSpace(assetID)] != nil, nil
}

func (r *Runtime) ResolveReadyAssetForSourceFile(
	ctx context.Context,
	sourceFileID string,
	kinds ...string,
) *commonv1.AssetRef {
	for _, kind := range kinds {
		ref, err := r.ReadyPublicAssetRefForSourceFile(ctx, r.db, sourceFileID, kind)
		if err == nil && ref != nil {
			return ref
		}
	}
	return nil
}

func (r *Runtime) ResolveArtistImageAsset(ctx context.Context, artistID string) *commonv1.AssetRef {
	var row struct {
		FileID string `gorm:"column:file_id"`
	}
	if err := r.db.WithContext(ctx).Raw(`
		SELECT file_id FROM artist_file WHERE artist_id = ?
		ORDER BY sort_order ASC LIMIT 1
	`, artistID).Scan(&row).Error; err != nil || row.FileID == "" {
		return nil
	}
	return r.ResolveReadyAssetForSourceFile(ctx, row.FileID, "image")
}

func normalizedAssetIDs(values ...*string) []string {
	ids := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == nil {
			continue
		}
		id := strings.TrimSpace(*value)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}
