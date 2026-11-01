package series

import (
	"context"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/failurecause"
)

// detail projects the ready OG asset and featured image of a Series. A
// featured image that fails to resolve is left out, as the Series is still
// worth serving.
func (s *Service) detail(ctx context.Context, series Series) (Detail, error) {
	ogAsset, err := s.media.ReadyAsset(ctx, series.OgAssetID)
	if err != nil {
		return Detail{}, err
	}
	detail := Detail{Series: series, OgAsset: ogAsset}
	if series.FeaturedImageFileID == nil || *series.FeaturedImageFileID == "" {
		return detail, nil
	}
	imageAsset, err := s.media.ReadyAssetForSourceFile(ctx, *series.FeaturedImageFileID, "image")
	if err != nil {
		slog.ErrorContext(ctx, "Series featured image asset resolution failed; serving Series without featured image", "domain", "series", "operation", "resolve_featured_image_asset", failurecause.Attr(err), "series_id", series.ID, "file_id", *series.FeaturedImageFileID)
		return detail, nil
	}
	detail.FeaturedImageAsset = imageAsset
	return detail, nil
}

func (s *Service) loadReadySeriesOgAssets(ctx context.Context, seriesList []Series) (map[string]*Asset, error) {
	candidates := make([]*string, 0, len(seriesList))
	for i := range seriesList {
		candidates = append(candidates, seriesList[i].OgAssetID)
	}
	return s.media.ReadyAssets(ctx, candidates...)
}
