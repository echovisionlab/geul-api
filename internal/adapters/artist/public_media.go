package artist

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	artistdomain "github.com/echovisionlab/geul-api/internal/artist"
	artistpublic "github.com/echovisionlab/geul-api/internal/artist/public"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// PublicMedia owns the concrete DB/CDN projection used by public Artist
// routes. Artist public policy sees only artist/public.MediaProjection.
type PublicMedia struct {
	db      *gorm.DB
	runtime *Runtime
}

func NewPublicMedia(db *gorm.DB, runtime *Runtime) *PublicMedia {
	if db == nil || runtime == nil {
		panic("Artist public media dependencies are required")
	}
	return &PublicMedia{db: db, runtime: runtime}
}

func (p *PublicMedia) LoadArtistImages(
	ctx context.Context,
	artistIDs []string,
) (map[string][]artistdomain.ArtistImageProjection, error) {
	return p.runtime.LoadArtistImages(ctx, p.db, artistIDs)
}

func (p *PublicMedia) ResolveReadyAssetRefs(
	ctx context.Context,
	candidates []*string,
) (map[string]*commonv1.AssetRef, error) {
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate != nil && strings.TrimSpace(*candidate) != "" {
			ids = append(ids, *candidate)
		}
	}
	return p.runtime.ResolveReadyAssetRefs(ctx, p.db, ids)
}

func (p *PublicMedia) ResolveArtistOGAsset(
	ctx context.Context,
	sourceAssetID *string,
	localizedAssetID *string,
) (*commonv1.AssetRef, error) {
	for _, candidate := range []*string{localizedAssetID, sourceAssetID} {
		if candidate == nil || strings.TrimSpace(*candidate) == "" {
			continue
		}
		ref, err := p.readyAssetRefByID(ctx, strings.TrimSpace(*candidate))
		if err == nil {
			return ref, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("resolve Artist OG asset: %w", err)
		}
	}
	return nil, nil
}

func (p *PublicMedia) IsReadyAsset(ctx context.Context, assetID string) (bool, error) {
	_, err := p.readyAssetRefByID(ctx, strings.TrimSpace(assetID))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (p *PublicMedia) readyAssetRefByID(ctx context.Context, assetID string) (*commonv1.AssetRef, error) {
	var asset model.PublicAsset
	if err := p.db.WithContext(ctx).
		Where("id = ? AND status = ?", assetID, model.PublicAssetStatusReady).
		Take(&asset).Error; err != nil {
		return nil, err
	}
	return mediaasset.NewLifecycle(p.db, p.runtime.cdnDomain).AssetRef(asset)
}

func (p *PublicMedia) LoadReleaseArtworkAssets(
	ctx context.Context,
	releaseIDs []string,
) (map[string]*commonv1.AssetRef, error) {
	result := make(map[string]*commonv1.AssetRef, len(releaseIDs))
	if len(releaseIDs) == 0 {
		return result, nil
	}
	var rows []struct {
		ReleaseID string `gorm:"column:release_id"`
		FileID    string `gorm:"column:file_id"`
	}
	if err := p.db.WithContext(ctx).Raw(`
		SELECT DISTINCT ON (release_id) release_id::text, file_id::text
		FROM release_file
		WHERE release_id IN ?
		ORDER BY release_id, sort_order, created_at, file_id
	`, releaseIDs).Scan(&rows).Error; err != nil {
		return nil, err
	}
	fileIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		fileIDs = append(fileIDs, row.FileID)
	}
	artworks, err := p.LoadReadySourceFileAssets(ctx, "artwork", fileIDs)
	if err != nil {
		return nil, err
	}
	images, err := p.LoadReadySourceFileAssets(ctx, "image", fileIDs)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.ReleaseID] = artworks[row.FileID]
		if result[row.ReleaseID] == nil {
			result[row.ReleaseID] = images[row.FileID]
		}
	}
	return result, nil
}

func (p *PublicMedia) LoadWorkImageAssets(
	ctx context.Context,
	workFileIDs map[string]*string,
) (map[string]*commonv1.AssetRef, error) {
	fileIDs := make([]string, 0, len(workFileIDs))
	for _, fileID := range workFileIDs {
		if fileID != nil {
			fileIDs = append(fileIDs, *fileID)
		}
	}
	assets, err := p.LoadReadySourceFileAssets(ctx, "image", fileIDs)
	if err != nil {
		return nil, err
	}
	result := make(map[string]*commonv1.AssetRef, len(workFileIDs))
	for workID, fileID := range workFileIDs {
		if fileID != nil {
			result[workID] = assets[*fileID]
		}
	}
	return result, nil
}

func (p *PublicMedia) LoadReadySourceFileAssets(
	ctx context.Context,
	kind string,
	fileIDs []string,
) (map[string]*commonv1.AssetRef, error) {
	return mediaasset.LoadReadyPublicAssetRefsForSourceFiles(
		ctx, p.db, p.runtime.cdnDomain, kind, fileIDs,
	)
}

var _ artistpublic.MediaProjection = (*PublicMedia)(nil)
