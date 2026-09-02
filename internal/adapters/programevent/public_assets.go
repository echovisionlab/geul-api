package programevent

import (
	"context"
	"errors"
	"strings"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"gorm.io/gorm"
)

// PublicAssets resolves ready Program Event public assets without exposing
// concrete public-asset persistence or CDN configuration to the public route.
type PublicAssets struct {
	db        *gorm.DB
	cdnDomain string
}

func NewPublicAssets(db *gorm.DB, cdnDomain string) *PublicAssets {
	if db == nil {
		panic("Program Event public assets: db is required")
	}
	return &PublicAssets{db: db, cdnDomain: strings.TrimRight(strings.TrimSpace(cdnDomain), "/")}
}

func (a *PublicAssets) ResolveReadyAssetForSourceFile(
	ctx context.Context,
	fileID string,
	kinds ...string,
) *commonv1.AssetRef {
	refs, err := a.ResolveReadyAssetsForSourceFiles(ctx, []string{fileID}, kinds...)
	if err != nil {
		return nil
	}
	return refs[strings.TrimSpace(fileID)]
}

func (a *PublicAssets) ResolveReadyAssetsForSourceFiles(
	ctx context.Context,
	fileIDs []string,
	kinds ...string,
) (map[string]*commonv1.AssetRef, error) {
	ids := uniqueNonEmptyIDs(fileIDs)
	result := make(map[string]*commonv1.AssetRef, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	query := a.db.WithContext(ctx).
		Where("source_file_id IN ? AND status = ?", ids, model.PublicAssetStatusReady)
	if len(kinds) > 0 {
		query = query.Where("kind IN ?", kinds)
	}
	var assets []model.PublicAsset
	if err := query.Order("created_at DESC, id DESC").Find(&assets).Error; err != nil {
		return nil, err
	}
	lifecycle := mediaasset.NewLifecycle(a.db, a.cdnDomain)
	for _, asset := range assets {
		if asset.SourceFileID == nil || result[*asset.SourceFileID] != nil {
			continue
		}
		if strings.TrimSpace(asset.ID) == "" || strings.TrimSpace(asset.Extension) == "" || strings.TrimSpace(asset.MimeType) == "" {
			return nil, errors.New("ready Program Event asset metadata is incomplete")
		}
		if asset.FileSize == nil || *asset.FileSize <= 0 || len(asset.SHA256) != 32 {
			return nil, errors.New("ready Program Event asset integrity metadata is incomplete")
		}
		if expected := model.GetExtensionFromMime(asset.MimeType); expected == "bin" || expected != asset.Extension {
			return nil, errors.New("ready Program Event asset extension does not match MIME type")
		}
		ref, err := lifecycle.AssetRef(asset)
		if err != nil {
			return nil, err
		}
		result[*asset.SourceFileID] = ref
	}
	return result, nil
}

func uniqueNonEmptyIDs(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
