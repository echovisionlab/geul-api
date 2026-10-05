package public

import (
	"context"

	artistdomain "github.com/echovisionlab/geul-api/internal/artist"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// MediaProjection is the public Artist boundary for CDN-backed assets and the
// related Release/Work read projections displayed on Artist routes.
type MediaProjection interface {
	LoadArtistImages(context.Context, []string) (map[string][]artistdomain.ArtistImageProjection, error)
	ResolveReadyAssetRefs(context.Context, []*string) (map[string]*commonv1.AssetRef, error)
	ResolveArtistOGAsset(context.Context, *string, *string) (*commonv1.AssetRef, error)
	IsReadyAsset(context.Context, string) (bool, error)
	LoadReleaseArtworkAssets(context.Context, []string) (map[string]*commonv1.AssetRef, error)
	LoadWorkImageAssets(context.Context, map[string]*string) (map[string]*commonv1.AssetRef, error)
	LoadReadySourceFileAssets(context.Context, string, []string) (map[string]*commonv1.AssetRef, error)
}
