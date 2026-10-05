package public

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

func TestReleaseSummaryUsesArtworkAsOgAndIgnoresLegacyGeneratedAsset(t *testing.T) {
	legacyOgAssetID := "legacy-generated-og"
	artwork := &commonv1.AssetRef{
		AssetId: "release-artwork",
		Url:     "https://cdn.example.test/asset/release-artwork/artwork.webp",
	}

	result := (&ReleaseService{}).toReleaseSummary(&model.Release{
		ID:        "release-1",
		Title:     "Release",
		Type:      "RELEASE_TYPE_ALBUM",
		OgAssetID: &legacyOgAssetID,
	}, artwork, nil)

	require.Same(t, artwork, result.ArtworkAsset)
	require.Same(t, artwork, result.OgAsset)
}

func TestReleaseSummaryHasNoOgWithoutArtwork(t *testing.T) {
	legacyOgAssetID := "legacy-generated-og"

	result := (&ReleaseService{}).toReleaseSummary(&model.Release{
		ID:        "release-1",
		Title:     "Release",
		Type:      "RELEASE_TYPE_ALBUM",
		OgAssetID: &legacyOgAssetID,
	}, nil, nil)

	require.Nil(t, result.ArtworkAsset)
	require.Nil(t, result.OgAsset)
}
