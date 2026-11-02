package post

import (
	fileref "github.com/echovisionlab/geul-api/internal/fileref"
	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mediaauth"
	optional "github.com/echovisionlab/geul-api/internal/optional"

	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPostSummaryFeaturedImagesUsePublicDisplayOnly(t *testing.T) {
	fileID := "11111111-1111-4111-8111-111111111111"
	files := &recordingPostFeaturedFiles{
		public: map[string]*fileref.Delivery{
			fileID: {
				FileID:    fileID,
				Inline:    &mediaauth.Reference{URL: "https://private.example/inline"},
				Download:  &mediaauth.Reference{URL: "https://private.example/download"},
				Thumbnail: &mediaasset.Reference{URL: "https://cdn.example/thumbnail.webp"},
			},
		},
	}
	service := &Service{fileService: files}

	got, err := service.loadPostPublicFeaturedImageDeliveries(t.Context(), []Post{{
		ID:                  "22222222-2222-4222-8222-222222222222",
		FeaturedImageFileID: &fileID,
	}})
	require.NoError(t, err)
	delivery := got["22222222-2222-4222-8222-222222222222"]
	require.NotNil(t, delivery)
	require.Nil(t, delivery.Inline)
	require.Nil(t, delivery.Download)
	require.Equal(t, "https://cdn.example/thumbnail.webp", optional.Value(optional.Value(delivery).Thumbnail).URL)
	require.Equal(t, []string{fileID}, files.publicFileIDs)
}

func TestPostSingularFeaturedImageAlwaysUsesExactOwnerBoundary(t *testing.T) {
	files := &recordingPostFeaturedFiles{}
	service := &Service{fileService: files}

	delivery, err := service.resolveAuthorizedPostFeaturedImage(t.Context(), &Post{
		ID: "22222222-2222-4222-8222-222222222222",
	})
	require.NoError(t, err)
	require.Nil(t, delivery)
	require.Equal(t, "22222222-2222-4222-8222-222222222222", files.exactPostID)
	require.Empty(t, files.exactFileID, "an empty slot still performs the final owner authorization")
}

type recordingPostFeaturedFiles struct {
	public        map[string]*fileref.Delivery
	publicFileIDs []string
	exactPostID   string
	exactFileID   string
}

func (*recordingPostFeaturedFiles) DeleteFileByID(context.Context, string) error { return nil }

func (f *recordingPostFeaturedFiles) ResolveAuthorizedPostFeaturedImage(
	_ context.Context,
	postID string,
	expectedFileID string,
) (*fileref.Delivery, error) {
	f.exactPostID = postID
	f.exactFileID = expectedFileID
	return nil, nil
}

func (f *recordingPostFeaturedFiles) ResolveDisplayMedia(
	_ context.Context,
	fileIDs []string,
) (map[string]*fileref.Delivery, error) {
	f.publicFileIDs = append([]string(nil), fileIDs...)
	return f.public, nil
}
