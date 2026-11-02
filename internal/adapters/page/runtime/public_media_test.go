package runtime

import (
	"context"
	"testing"

	fileref "github.com/echovisionlab/geul-api/internal/fileref"

	"github.com/echovisionlab/geul-api/internal/mediaauth"

	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"

	optional "github.com/echovisionlab/geul-api/internal/optional"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/stretchr/testify/require"
)

type pagePublicFilesFixture struct {
	displayIDs  []string
	sourceOG    *string
	localizedOG *string
}

func (f *pagePublicFilesFixture) HydrateAuthorizedContentBlockMedia(
	_ context.Context,
	items []*contentv1.ContentBlockMediaItem,
) ([]*contentv1.ContentBlockMediaItem, error) {
	return items, nil
}

func (f *pagePublicFilesFixture) ResolveDisplayMedia(
	_ context.Context,
	fileIDs []string,
) (map[string]*fileref.Delivery, error) {
	f.displayIDs = append([]string(nil), fileIDs...)
	result := make(map[string]*fileref.Delivery, len(fileIDs))
	for _, fileID := range fileIDs {
		asset := &mediaasset.Reference{AssetID: "asset-" + fileID}
		result[fileID] = &fileref.Delivery{
			FileID: fileID, Asset: asset, Thumbnail: asset,
			Inline:   &mediaauth.Reference{URL: "private-inline"},
			Download: &mediaauth.Reference{URL: "private-download"},
		}
	}
	return result, nil
}

func (f *pagePublicFilesFixture) ResolveOGAsset(
	_ context.Context,
	sourceAssetID *string,
	localizedAssetID *string,
) (*mediaasset.Reference, error) {
	f.sourceOG = sourceAssetID
	f.localizedOG = localizedAssetID
	return &mediaasset.Reference{AssetID: "asset-1"}, nil
}

func TestPublicMediaResolvesExactPageFeaturedFile(t *testing.T) {
	fixture := &pagePublicFilesFixture{}
	adapter := NewPublicMedia(fixture)
	fileID := " file-1 "

	delivery, err := adapter.ResolvePageFeaturedImageDelivery(t.Context(), &fileID)
	require.NoError(t, err)
	require.Equal(t, "file-1", optional.Value(delivery).FileID)
	require.Equal(t, "asset-file-1", optional.Value(optional.Value(delivery).Asset).AssetID)
	require.Equal(t, optional.Value(delivery).Asset, optional.Value(delivery).Thumbnail)
	require.Nil(t, optional.Value(delivery).Inline)
	require.Nil(t, optional.Value(delivery).Download)
	require.Equal(t, []string{"file-1"}, fixture.displayIDs)
}

func TestPublicMediaDelegatesPageOGFallbackOrder(t *testing.T) {
	fixture := &pagePublicFilesFixture{}
	adapter := NewPublicMedia(fixture)
	source := "source-asset"
	localized := "localized-asset"

	asset, err := adapter.ResolvePageOGAsset(t.Context(), &source, &localized)
	require.NoError(t, err)
	require.Equal(t, "asset-1", optional.Value(asset).AssetID)
	require.Equal(t, &source, fixture.sourceOG)
	require.Equal(t, &localized, fixture.localizedOG)
}
