package public

import "context"

func (s *ArtistService) localizedOGAssetReady(ctx context.Context, assetID string) (bool, error) {
	return s.media.IsReadyAsset(ctx, assetID)
}
