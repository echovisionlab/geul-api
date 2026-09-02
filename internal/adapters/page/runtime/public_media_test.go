package runtime

import (
	"context"
	"testing"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
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

func (f *pagePublicFilesFixture) ResolvePublicDisplayMedia(
	_ context.Context,
	fileIDs []string,
) (map[string]*commonv1.MediaDelivery, error) {
	f.displayIDs = append([]string(nil), fileIDs...)
	result := make(map[string]*commonv1.MediaDelivery, len(fileIDs))
	for _, fileID := range fileIDs {
		asset := &commonv1.AssetRef{AssetId: "asset-" + fileID}
		result[fileID] = &commonv1.MediaDelivery{
			FileId: fileID, Asset: asset, Thumbnail: asset,
			Inline:   &commonv1.ExpiringMediaRef{Url: "private-inline"},
			Download: &commonv1.ExpiringMediaRef{Url: "private-download"},
		}
	}
	return result, nil
}

func (f *pagePublicFilesFixture) ResolveReadyOGAsset(
	_ context.Context,
	sourceAssetID *string,
	localizedAssetID *string,
) (*commonv1.AssetRef, error) {
	f.sourceOG = sourceAssetID
	f.localizedOG = localizedAssetID
	return &commonv1.AssetRef{AssetId: "asset-1"}, nil
}

func TestPublicMediaResolvesExactPageFeaturedFile(t *testing.T) {
	fixture := &pagePublicFilesFixture{}
	adapter := NewPublicMedia(fixture)
	fileID := " file-1 "

	delivery, err := adapter.ResolvePageFeaturedImageDelivery(t.Context(), &fileID)
	require.NoError(t, err)
	require.Equal(t, "file-1", delivery.GetFileId())
	require.Equal(t, "asset-file-1", delivery.GetAsset().GetAssetId())
	require.Equal(t, delivery.GetAsset(), delivery.GetThumbnail())
	require.Nil(t, delivery.GetInline())
	require.Nil(t, delivery.GetDownload())
	require.Equal(t, []string{"file-1"}, fixture.displayIDs)
}

func TestPublicMediaDelegatesPageOGFallbackOrder(t *testing.T) {
	fixture := &pagePublicFilesFixture{}
	adapter := NewPublicMedia(fixture)
	source := "source-asset"
	localized := "localized-asset"

	asset, err := adapter.ResolvePageOGAsset(t.Context(), &source, &localized)
	require.NoError(t, err)
	require.Equal(t, "asset-1", asset.GetAssetId())
	require.Equal(t, &source, fixture.sourceOG)
	require.Equal(t, &localized, fixture.localizedOG)
}
