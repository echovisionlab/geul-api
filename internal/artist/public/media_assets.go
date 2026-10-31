package public

import "context"

func (s *Service) localizedOGAssetReady(ctx context.Context, assetID string) (bool, error) {
	return s.media.IsReadyAsset(ctx, assetID)
}
