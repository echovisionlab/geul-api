package series

import (
	"context"
	"strings"

	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"
	seriesdomain "github.com/echovisionlab/geul-api/internal/series"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// AssetFromRef is the Series form of a ready asset; nil stays nil.
func AssetFromRef(ref *mediaasset.Reference) *seriesdomain.Asset {
	if ref == nil {
		return nil
	}
	return &seriesdomain.Asset{
		ID:               ref.AssetID,
		URL:              ref.URL,
		Extension:        ref.Extension,
		MIMEType:         ref.MimeType,
		FileSize:         ref.FileSize,
		SHA256:           ref.SHA256,
		Disposition:      seriesdomain.AssetDisposition(ref.Disposition),
		DownloadFilename: ref.DownloadFilename,
	}
}

func assetsFromRefs(refs map[string]*mediaasset.Reference) map[string]*seriesdomain.Asset {
	if refs == nil {
		return nil
	}
	assets := make(map[string]*seriesdomain.Asset, len(refs))
	for id, ref := range refs {
		assets[id] = AssetFromRef(ref)
	}
	return assets
}

type Runtime struct {
	db          *gorm.DB
	cdnDomain   string
	ogRefresher *ogpostgres.Refresher
}

func NewRuntime(db *gorm.DB, cdnDomain string, ogRefresher *ogpostgres.Refresher) *Runtime {
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

func (r *Runtime) ReadyAsset(ctx context.Context, candidateIDs ...*string) (*seriesdomain.Asset, error) {
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

func (r *Runtime) ReadyAssets(ctx context.Context, candidateIDs ...*string) (map[string]*seriesdomain.Asset, error) {
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
	refs := make(map[string]*seriesdomain.Asset, len(assetIDs))
	if len(assetIDs) == 0 {
		return refs, nil
	}
	var assets []model.PublicAsset
	if err := r.db.WithContext(ctx).
		Where("id IN ? AND status = ?", assetIDs, model.PublicAssetStatusReady).
		Find(&assets).Error; err != nil {
		return nil, err
	}
	lifecycle := mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(r.db), r.cdnDomain)
	for _, asset := range assets {
		if asset.FileSize == nil || len(asset.SHA256) != 32 {
			continue
		}
		ref, err := lifecycle.AssetRef(mediaassetpostgres.Asset(asset))
		if err != nil {
			return nil, err
		}
		refs[asset.ID] = AssetFromRef(ref)
	}
	return refs, nil
}

func (r *Runtime) ReadyAssetsForSourceFiles(
	ctx context.Context,
	kind string,
	fileIDs []string,
) (map[string]*seriesdomain.Asset, error) {
	refs, err := mediaasset.LoadReadyPublicAssetRefsForSourceFiles(ctx, mediaassetpostgres.NewUnits(r.db), r.cdnDomain, kind, fileIDs)
	if err != nil {
		return nil, err
	}
	return assetsFromRefs(refs), nil
}

func (r *Runtime) ReadyAssetForSourceFile(ctx context.Context, fileID, kind string) (*seriesdomain.Asset, error) {
	ref, err := mediaasset.ReadyPublicAssetRefForSourceFile(ctx, mediaassetpostgres.NewUnits(r.db), r.cdnDomain, fileID, kind)
	return AssetFromRef(ref), err
}

func (r *Runtime) BindFeaturedImage(
	ctx context.Context,
	tx *gorm.DB,
	fileID string,
	seriesID string,
) (*seriesdomain.Asset, error) {
	ref, err := mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(tx), r.cdnDomain).
		BindReadyAssetForSourceFile(ctx, fileID, "series", seriesID, "featured_image", "image")
	return AssetFromRef(ref), err
}

func (r *Runtime) RequireAttachableFile(ctx context.Context, tx *gorm.DB, fileID string) error {
	if err := mediaassetpostgres.NewStore(tx).LockAttachableFiles(ctx, []string{fileID}); err != nil {
		return err
	}
	var file model.File
	if err := tx.WithContext(ctx).First(&file, "id = ?", fileID).Error; err != nil {
		return err
	}
	return nil
}

func (r *Runtime) ReleaseFeaturedImage(ctx context.Context, tx *gorm.DB, seriesID string) error {
	return mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(tx), r.cdnDomain).
		ReleasePublicAssetBindings(ctx, "series", seriesID, "featured_image")
}

func (r *Runtime) CancelAndReleaseOG(ctx context.Context, tx *gorm.DB, seriesID string) error {
	if err := ogpostgres.CancelEntity(
		ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_SERIES, seriesID,
	); err != nil {
		return err
	}
	return mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(tx), r.cdnDomain).
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
