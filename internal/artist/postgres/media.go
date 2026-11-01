package artistpostgres

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/artist"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (s *store) Images(ctx context.Context, artistIDs []string) (map[string][]artist.ArtistImageProjection, error) {
	return s.ports.Runtime.LoadArtistImages(ctx, s.tx, artistIDs)
}

func (s *store) ReplaceImageBindings(ctx context.Context, artistID string, fileIDs []string) error {
	return s.ports.Runtime.ReplaceArtistImageBindings(ctx, s.tx, artistID, fileIDs)
}

func (s *store) ReleaseImageBindings(ctx context.Context, artistID string) error {
	return s.ports.Runtime.ReleasePublicAssetBindings(ctx, s.tx, "artist", artistID, "image")
}

func (s *store) ReadyAssets(ctx context.Context, assetIDs []string) (map[string]*commonv1.AssetRef, error) {
	return s.ports.Runtime.ResolveReadyAssetRefs(ctx, s.tx, assetIDs)
}

func (s *store) RequestOG(ctx context.Context, artistID, locale string, sourceChanged bool, reason string) (string, error) {
	return s.ports.Runtime.RequestCurrentWithDB(ctx, s.tx, managev1.OgEntityType_OG_ENTITY_TYPE_ARTIST, artistID, locale, sourceChanged, reason)
}

func (s *store) CancelOG(ctx context.Context, artistID string) error {
	return s.ports.Runtime.CancelAndReleaseEntityWithDB(ctx, s.tx, managev1.OgEntityType_OG_ENTITY_TYPE_ARTIST, "artist", artistID)
}
