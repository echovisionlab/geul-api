package public

import (
	fileref "github.com/echovisionlab/geul-api/internal/fileref"
	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mediaauth"
	optional "github.com/echovisionlab/geul-api/internal/optional"

	"context"
	"testing"

	"github.com/echovisionlab/geul-api/internal/post"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type publicDisplayPostFiles struct {
	deliveries map[string]*fileref.Delivery
}

func (f publicDisplayPostFiles) ResolveDisplayMedia(
	context.Context,
	[]string,
) (map[string]*fileref.Delivery, error) {
	return f.deliveries, nil
}

func (publicDisplayPostFiles) ResolveAuthorizedContentBlockMedia(
	context.Context,
	uuid.UUID,
) ([]*fileref.BlockMediaItem, error) {
	return nil, nil
}

func TestLoadPostFeaturedImageDeliveriesKeepsOnlyPublicDisplayProjection(t *testing.T) {
	fileID := uuid.NewString()
	asset := &mediaasset.Reference{AssetID: uuid.NewString(), URL: "https://cdn.example.test/asset.webp"}
	service := &Service{files: publicDisplayPostFiles{deliveries: map[string]*fileref.Delivery{
		fileID: {
			FileID: fileID, Asset: asset, Thumbnail: asset,
			Inline:   &mediaauth.Reference{URL: "private-inline"},
			Download: &mediaauth.Reference{URL: "private-download"},
		},
	}}}

	got, err := service.loadPostFeaturedImageDeliveries(t.Context(), []post.Post{{
		ID: uuid.NewString(), FeaturedImageFileID: &fileID,
	}})
	require.NoError(t, err)
	require.Len(t, got, 1)
	for _, delivery := range got {
		require.Equal(t, asset, optional.Value(delivery).Asset)
		require.Equal(t, asset, optional.Value(delivery).Thumbnail)
		require.Nil(t, optional.Value(delivery).Inline)
		require.Nil(t, optional.Value(delivery).Download)
	}
}
