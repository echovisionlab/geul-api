package public

import (
	"context"
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type publicDisplayPostFiles struct {
	deliveries map[string]*commonv1.MediaDelivery
}

func (f publicDisplayPostFiles) ResolvePublicDisplayMedia(
	context.Context,
	[]string,
) (map[string]*commonv1.MediaDelivery, error) {
	return f.deliveries, nil
}

func (publicDisplayPostFiles) ResolveAuthorizedContentBlockMedia(
	context.Context,
	uuid.UUID,
) ([]*contentv1.ContentBlockMediaItem, error) {
	return nil, nil
}

func TestLoadPostFeaturedImageDeliveriesKeepsOnlyPublicDisplayProjection(t *testing.T) {
	fileID := uuid.NewString()
	asset := &commonv1.AssetRef{AssetId: uuid.NewString(), Url: "https://cdn.example.test/asset.webp"}
	service := &PostService{files: publicDisplayPostFiles{deliveries: map[string]*commonv1.MediaDelivery{
		fileID: {
			FileId: fileID, Asset: asset, Thumbnail: asset,
			Inline:   &commonv1.ExpiringMediaRef{Url: "private-inline"},
			Download: &commonv1.ExpiringMediaRef{Url: "private-download"},
		},
	}}}

	got, err := service.loadPostFeaturedImageDeliveries(t.Context(), []model.Post{{
		ID: uuid.NewString(), FeaturedImageFileID: &fileID,
	}})
	require.NoError(t, err)
	require.Len(t, got, 1)
	for _, delivery := range got {
		require.Equal(t, asset, delivery.GetAsset())
		require.Equal(t, asset, delivery.GetThumbnail())
		require.Nil(t, delivery.GetInline())
		require.Nil(t, delivery.GetDownload())
	}
}
