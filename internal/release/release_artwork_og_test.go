package release

import (
	"testing"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"

	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/stretchr/testify/require"
)

func TestReleaseProtoUsesArtworkAsOgAndIgnoresLegacyGeneratedAsset(t *testing.T) {
	legacyOgAssetID := "legacy-generated-og"
	artwork := &mediaasset.Reference{
		AssetID: "release-artwork",
		URL:     "https://cdn.example.test/asset/release-artwork/artwork.webp",
	}

	result := ReleaseMessage(View{Release: Release{
		ID: "release-1", Type: TypeAlbum, Status: StatusPublished, OgAssetID: &legacyOgAssetID,
	}, Title: "Release", Artwork: artwork})

	require.Equal(t, &commonv1.AssetRef{AssetId: "release-artwork", Url: "https://cdn.example.test/asset/release-artwork/artwork.webp"}, result.ArtworkAsset)
	require.Same(t, result.ArtworkAsset, result.OgAsset)
}

func TestReleaseProtoHasNoOgWithoutArtwork(t *testing.T) {
	legacyOgAssetID := "legacy-generated-og"

	result := ReleaseMessage(View{Release: Release{
		ID: "release-1", Type: TypeAlbum, Status: StatusPublished, OgAssetID: &legacyOgAssetID,
	}, Title: "Release"})

	require.Nil(t, result.ArtworkAsset)
	require.Nil(t, result.OgAsset)
}
