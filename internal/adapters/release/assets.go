package release

import (
	"context"
	"strings"

	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	releasepostgres "github.com/echovisionlab/geul-api/internal/release/postgres"
	"gorm.io/gorm"
)

type Assets struct {
	cdnDomain string
}

func NewAssets(cdnDomain string) *Assets {
	return &Assets{cdnDomain: strings.TrimSpace(cdnDomain)}
}

func (*Assets) LockAttachableFiles(ctx context.Context, tx *gorm.DB, fileIDs []string) error {
	return mediaassetpostgres.NewStore(tx).LockAttachableFiles(ctx, fileIDs)
}

func (a *Assets) BindArtwork(
	ctx context.Context,
	tx *gorm.DB,
	fileID, releaseID string,
) (*mediaasset.Reference, error) {
	return mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(tx), a.cdnDomain).BindReadyAssetForSourceFile(
		ctx, fileID, "release", releaseID, "artwork", "artwork",
	)
}

func (a *Assets) ReleaseArtwork(ctx context.Context, tx *gorm.DB, releaseID string) error {
	return mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(tx), a.cdnDomain).
		ReleasePublicAssetBindings(ctx, "release", releaseID, "artwork")
}

func (a *Assets) ReadyArtwork(
	ctx context.Context,
	db *gorm.DB,
	fileID string,
) (*mediaasset.Reference, error) {
	return mediaasset.ReadyPublicAssetRefForSourceFile(ctx, mediaassetpostgres.NewUnits(db), a.cdnDomain, fileID, "artwork")
}

func (a *Assets) ReadyAssets(
	ctx context.Context,
	db *gorm.DB,
	candidateIDs ...*string,
) (map[string]*mediaasset.Reference, error) {
	assetIDs := normalizedAssetIDs(candidateIDs)
	ready := make(map[string]*mediaasset.Reference, len(assetIDs))
	if len(assetIDs) == 0 {
		return ready, nil
	}

	var assets []model.PublicAsset
	if err := db.WithContext(ctx).
		Where("id IN ? AND status = ?", assetIDs, model.PublicAssetStatusReady).
		Find(&assets).Error; err != nil {
		return nil, errs.Internal(err)
	}

	lifecycle := mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(db), a.cdnDomain)
	for _, asset := range assets {
		if asset.FileSize == nil || len(asset.SHA256) != 32 {
			continue
		}
		ref, err := lifecycle.AssetRef(mediaassetpostgres.Asset(asset))
		if err != nil {
			return nil, err
		}
		ready[asset.ID] = ref
	}
	return ready, nil
}

func normalizedAssetIDs(candidateIDs []*string) []string {
	seen := make(map[string]struct{}, len(candidateIDs))
	ids := make([]string, 0, len(candidateIDs))
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
		ids = append(ids, id)
	}
	return ids
}

var _ releasepostgres.Assets = (*Assets)(nil)
