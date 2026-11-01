package release

import (
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

func TestReleaseProtoUsesArtworkAsOgAndIgnoresLegacyGeneratedAsset(t *testing.T) {
	legacyOgAssetID := "legacy-generated-og"
	artwork := &commonv1.AssetRef{
		AssetId: "release-artwork",
		Url:     "https://cdn.example.test/asset/release-artwork/artwork.webp",
	}

	result := ReleaseMessage(View{Release: Release{
		ID: "release-1", Type: TypeAlbum, Status: StatusPublished, OgAssetID: &legacyOgAssetID,
	}, Title: "Release", Artwork: artwork})

	require.Same(t, artwork, result.ArtworkAsset)
	require.Same(t, artwork, result.OgAsset)
}

func TestReleaseProtoHasNoOgWithoutArtwork(t *testing.T) {
	legacyOgAssetID := "legacy-generated-og"

	result := ReleaseMessage(View{Release: Release{
		ID: "release-1", Type: TypeAlbum, Status: StatusPublished, OgAssetID: &legacyOgAssetID,
	}, Title: "Release"})

	require.Nil(t, result.ArtworkAsset)
	require.Nil(t, result.OgAsset)
}
