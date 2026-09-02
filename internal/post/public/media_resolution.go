package public

import (
	"context"
	"strings"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"google.golang.org/protobuf/proto"
)

func publicDisplayOnlyDelivery(delivery *commonv1.MediaDelivery) *commonv1.MediaDelivery {
	if delivery == nil {
		return nil
	}
	result := proto.Clone(delivery).(*commonv1.MediaDelivery)
	result.Inline = nil
	result.Download = nil
	return result
}

func (s *PostService) loadPostFeaturedImageDeliveries(
	ctx context.Context,
	posts []model.Post,
) (map[string]*commonv1.MediaDelivery, error) {
	result := make(map[string]*commonv1.MediaDelivery, len(posts))
	fileIDs := make([]string, 0, len(posts))
	postIDsByFileID := make(map[string][]string, len(posts))
	for i := range posts {
		if posts[i].FeaturedImageFileID == nil {
			continue
		}
		fileID := strings.TrimSpace(*posts[i].FeaturedImageFileID)
		if fileID == "" {
			continue
		}
		if _, exists := postIDsByFileID[fileID]; !exists {
			fileIDs = append(fileIDs, fileID)
		}
		postIDsByFileID[fileID] = append(postIDsByFileID[fileID], posts[i].ID)
	}
	if len(fileIDs) == 0 {
		return result, nil
	}
	if s.files == nil {
		return nil, errs.InternalMsg("Post media resolver is not configured")
	}
	deliveries, err := s.files.ResolvePublicDisplayMedia(ctx, fileIDs)
	if err != nil {
		return nil, err
	}
	for fileID, postIDs := range postIDsByFileID {
		delivery := publicDisplayOnlyDelivery(deliveries[fileID])
		if delivery == nil {
			continue
		}
		for _, postID := range postIDs {
			result[postID] = delivery
		}
	}
	return result, nil
}
