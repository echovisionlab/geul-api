package favicon_test

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/favicon"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/stretchr/testify/require"
)

func TestSetProtoPreservesEachFaviconSlotAndMissingSVG(t *testing.T) {
	require.Nil(t, favicon.SetProto(nil))
	set := &favicon.Set{IconICO: &mediaasset.Reference{AssetID: "ico"}, IconPNG16: &mediaasset.Reference{AssetID: "16"}, IconPNG32: &mediaasset.Reference{AssetID: "32"}, IconPNG48: &mediaasset.Reference{AssetID: "48"}, AppleTouch180: &mediaasset.Reference{AssetID: "180"}, Manifest192: &mediaasset.Reference{AssetID: "192"}, Manifest512: &mediaasset.Reference{AssetID: "512"}}
	wire := favicon.SetProto(set)
	require.Equal(t, []string{"ico", "16", "32", "48", "180", "192", "512"}, []string{wire.GetIconIco().GetAssetId(), wire.GetIconPng_16().GetAssetId(), wire.GetIconPng_32().GetAssetId(), wire.GetIconPng_48().GetAssetId(), wire.GetAppleTouchIcon_180().GetAssetId(), wire.GetManifestIcon_192().GetAssetId(), wire.GetManifestIcon_512().GetAssetId()})
	require.Nil(t, wire.IconSvg)
	set.IconSVG = &mediaasset.Reference{AssetID: "svg"}
	require.Equal(t, "svg", favicon.SetProto(set).GetIconSvg().GetAssetId())
}
