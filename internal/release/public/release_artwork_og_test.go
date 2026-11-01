package public

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/release"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

func TestReleaseSummaryUsesArtworkAsOgAndIgnoresLegacyGeneratedAsset(t *testing.T) {
	legacyOgAssetID := "legacy-generated-og"
	artwork := &commonv1.AssetRef{
		AssetId: "release-artwork",
		Url:     "https://cdn.example.test/asset/release-artwork/artwork.webp",
	}

	result := releaseSummaryMessage(Summary{Release: release.Release{
		ID: "release-1", Type: release.TypeAlbum, OgAssetID: &legacyOgAssetID,
	}, Title: "Release", Artwork: artwork})

	require.Same(t, artwork, result.ArtworkAsset)
	require.Same(t, artwork, result.OgAsset)
}

func TestReleaseSummaryHasNoOgWithoutArtwork(t *testing.T) {
	legacyOgAssetID := "legacy-generated-og"

	result := releaseSummaryMessage(Summary{Release: release.Release{
		ID: "release-1", Type: release.TypeAlbum, OgAssetID: &legacyOgAssetID,
	}, Title: "Release"})

	require.Nil(t, result.ArtworkAsset)
	require.Nil(t, result.OgAsset)
}
