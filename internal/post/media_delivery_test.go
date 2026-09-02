package post

import (
	"context"
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"github.com/stretchr/testify/require"
)

func TestPostSummaryFeaturedImagesUsePublicDisplayOnly(t *testing.T) {
	fileID := "11111111-1111-4111-8111-111111111111"
	files := &recordingPostFeaturedFiles{
		public: map[string]*commonv1.MediaDelivery{
			fileID: {
				FileId:    fileID,
				Inline:    &commonv1.ExpiringMediaRef{Url: "https://private.example/inline"},
				Download:  &commonv1.ExpiringMediaRef{Url: "https://private.example/download"},
				Thumbnail: &commonv1.AssetRef{Url: "https://cdn.example/thumbnail.webp"},
			},
		},
	}
	service := &PostService{fileService: files}

	got, err := service.loadPostPublicFeaturedImageDeliveries(t.Context(), []model.Post{{
		ID:                  "22222222-2222-4222-8222-222222222222",
		FeaturedImageFileID: &fileID,
	}})
	require.NoError(t, err)
	delivery := got["22222222-2222-4222-8222-222222222222"]
	require.NotNil(t, delivery)
	require.Nil(t, delivery.Inline)
	require.Nil(t, delivery.Download)
	require.Equal(t, "https://cdn.example/thumbnail.webp", delivery.GetThumbnail().GetUrl())
	require.Equal(t, []string{fileID}, files.publicFileIDs)
}

func TestPostSingularFeaturedImageAlwaysUsesExactOwnerBoundary(t *testing.T) {
	files := &recordingPostFeaturedFiles{}
	service := &PostService{fileService: files}

	delivery, err := service.resolveAuthorizedPostFeaturedImage(t.Context(), &model.Post{
		ID: "22222222-2222-4222-8222-222222222222",
	})
	require.NoError(t, err)
	require.Nil(t, delivery)
	require.Equal(t, "22222222-2222-4222-8222-222222222222", files.exactPostID)
	require.Empty(t, files.exactFileID, "an empty slot still performs the final owner authorization")
}

type recordingPostFeaturedFiles struct {
	public        map[string]*commonv1.MediaDelivery
	publicFileIDs []string
	exactPostID   string
	exactFileID   string
}

func (*recordingPostFeaturedFiles) DeleteFileByID(context.Context, string) error { return nil }

func (f *recordingPostFeaturedFiles) ResolveAuthorizedPostFeaturedImage(
	_ context.Context,
	postID string,
	expectedFileID string,
) (*commonv1.MediaDelivery, error) {
	f.exactPostID = postID
	f.exactFileID = expectedFileID
	return nil, nil
}

func (f *recordingPostFeaturedFiles) ResolvePublicDisplayMedia(
	_ context.Context,
	fileIDs []string,
) (map[string]*commonv1.MediaDelivery, error) {
	f.publicFileIDs = append([]string(nil), fileIDs...)
	return f.public, nil
}
