package artist

import (
	"context"

	"connectrpc.com/connect"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// ArtistService implements the ArtistService Connect handler
func (s *ArtistService) SetArtistImage(
	ctx context.Context,
	req *connect.Request[managev1.SetArtistImageRequest],
) (*connect.Response[managev1.SetArtistImageResponse], error) {
	fileIDs, err := validateOrderedArtistFileIDs([]string{req.Msg.FileId})
	if err != nil {
		return nil, err
	}
	images, ogRunID, changed, err := s.replaceArtistImages(ctx, req.Msg.ArtistId, fileIDs, nil)
	if err != nil {
		return nil, err
	}

	if changed {
		publishContentUpdatedEvent(
			ctx,
			s.asyncPublisher,
			buildManageMediaMutationContentUpdatedEvent(
				managev1.ContentEntityType_CONTENT_ENTITY_TYPE_ARTIST,
				req.Msg.ArtistId,
				"media.images",
			),
		)
	}
	var imageAsset *commonv1.AssetRef
	if len(images) > 0 {
		imageAsset = images[0].Asset
	}
	return connect.NewResponse(&managev1.SetArtistImageResponse{
		ImageAsset: imageAsset, OgGenerationRunId: ogRunID,
	}), nil
}

// DeleteArtistImage removes the artist's profile image (admin or manager)
func (s *ArtistService) DeleteArtistImage(
	ctx context.Context,
	req *connect.Request[managev1.DeleteArtistImageRequest],
) (*connect.Response[managev1.OgAssetDeleteResponse], error) {
	_, ogRunID, changed, err := s.replaceArtistImages(ctx, req.Msg.ArtistId, nil, nil)
	if err != nil {
		return nil, err
	}
	if changed {
		publishContentUpdatedEvent(
			ctx,
			s.asyncPublisher,
			buildManageMediaMutationContentUpdatedEvent(
				managev1.ContentEntityType_CONTENT_ENTITY_TYPE_ARTIST,
				req.Msg.ArtistId,
				"media.images",
			),
		)
	}

	return connect.NewResponse(&managev1.OgAssetDeleteResponse{
		Success: true, OgGenerationRunId: ogRunID,
	}), nil
}

// ==================== Owner and Manager Assignment ====================
