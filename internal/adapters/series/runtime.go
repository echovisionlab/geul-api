package series

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

type Runtime struct {
	db          *gorm.DB
	cdnDomain   string
	ogRefresher *og.Refresher
}

func NewRuntime(db *gorm.DB, cdnDomain string, ogRefresher *og.Refresher) *Runtime {
	if db == nil || ogRefresher == nil {
		panic("series runtime: database and OG refresher are required")
	}
	return &Runtime{db: db, cdnDomain: strings.TrimSpace(cdnDomain), ogRefresher: ogRefresher}
}

func (r *Runtime) LoadPostCounts(ctx context.Context, seriesIDs []string) (map[string]int32, error) {
	return NewReadProjection(r.db).LoadPostCounts(ctx, seriesIDs)
}

func (r *Runtime) LoadManagerCounts(ctx context.Context, seriesIDs []string) (map[string]int32, error) {
	return NewReadProjection(r.db).LoadManagerCounts(ctx, seriesIDs)
}

func (r *Runtime) ReadyAsset(ctx context.Context, candidateIDs ...*string) (*commonv1.AssetRef, error) {
	ready, err := r.ReadyAssets(ctx, candidateIDs...)
	if err != nil {
		return nil, err
	}
	for _, candidateID := range candidateIDs {
		if candidateID == nil {
			continue
		}
		if ref := ready[strings.TrimSpace(*candidateID)]; ref != nil {
			return ref, nil
		}
	}
	return nil, nil
}

func (r *Runtime) ReadyAssets(ctx context.Context, candidateIDs ...*string) (map[string]*commonv1.AssetRef, error) {
	seen := make(map[string]struct{}, len(candidateIDs))
	assetIDs := make([]string, 0, len(candidateIDs))
	for _, candidateID := range candidateIDs {
		if candidateID == nil {
			continue
		}
		id := strings.TrimSpace(*candidateID)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		assetIDs = append(assetIDs, id)
	}
	refs := make(map[string]*commonv1.AssetRef, len(assetIDs))
	if len(assetIDs) == 0 {
		return refs, nil
	}
	var assets []model.PublicAsset
	if err := r.db.WithContext(ctx).
		Where("id IN ? AND status = ?", assetIDs, model.PublicAssetStatusReady).
		Find(&assets).Error; err != nil {
		return nil, err
	}
	lifecycle := mediaasset.NewLifecycle(r.db, r.cdnDomain)
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

func (r *Runtime) ReadyAssetsForSourceFiles(
	ctx context.Context,
	kind string,
	fileIDs []string,
) (map[string]*commonv1.AssetRef, error) {
	return mediaasset.LoadReadyPublicAssetRefsForSourceFiles(ctx, r.db, r.cdnDomain, kind, fileIDs)
}

func (r *Runtime) ReadyAssetForSourceFile(ctx context.Context, fileID, kind string) (*commonv1.AssetRef, error) {
	return mediaasset.ReadyPublicAssetRefForSourceFile(ctx, r.db, r.cdnDomain, fileID, kind)
}

func (r *Runtime) BindFeaturedImage(
	ctx context.Context,
	tx *gorm.DB,
	fileID string,
	seriesID string,
) (*commonv1.AssetRef, error) {
	return mediaasset.NewLifecycle(tx, r.cdnDomain).
		BindReadyAssetForSourceFile(ctx, fileID, "series", seriesID, "featured_image", "image")
}

func (r *Runtime) RequireAttachableFile(ctx context.Context, tx *gorm.DB, fileID string) error {
	if err := mediaasset.LockAttachableFilesForUpdate(ctx, tx, []string{fileID}); err != nil {
		return err
	}
	var file model.File
	if err := tx.WithContext(ctx).First(&file, "id = ?", fileID).Error; err != nil {
		return err
	}
	return nil
}

func (r *Runtime) ReleaseFeaturedImage(ctx context.Context, tx *gorm.DB, seriesID string) error {
	return mediaasset.NewLifecycle(tx, r.cdnDomain).
		ReleasePublicAssetBindings(ctx, "series", seriesID, "featured_image")
}

func (r *Runtime) CancelAndReleaseOG(ctx context.Context, tx *gorm.DB, seriesID string) error {
	if err := og.NewLifecycle(tx, r.cdnDomain).CancelEntityWithDB(
		ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_SERIES, seriesID,
	); err != nil {
		return err
	}
	return mediaasset.NewLifecycle(tx, r.cdnDomain).
		ReleasePublicAssetBindings(ctx, "series", seriesID, "og")
}

func (r *Runtime) RequestCurrent(
	ctx context.Context,
	tx *gorm.DB,
	entityID string,
	locale string,
	allLocales bool,
	reason string,
) (*string, error) {
	plan, err := r.ogRefresher.RequestCurrentWithDB(
		ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_SERIES,
		entityID, locale, allLocales, reason,
	)
	if err != nil || plan == nil {
		return nil, err
	}
	return &plan.RunID, nil
}
