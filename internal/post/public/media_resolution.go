package public

import (
	"context"
	"errors"
	"strings"

	fileref "github.com/echovisionlab/geul-api/internal/fileref"

	"github.com/echovisionlab/geul-api/internal/post"
)

func publicDisplayOnlyDelivery(delivery *fileref.Delivery) *fileref.Delivery {
	if delivery == nil {
		return nil
	}
	result := delivery.Clone()
	result.Inline = nil
	result.Download = nil
	return result
}

func (s *Service) loadPostFeaturedImageDeliveries(
	ctx context.Context,
	posts []post.Post,
) (map[string]*fileref.Delivery, error) {
	result := make(map[string]*fileref.Delivery, len(posts))
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
		return nil, errors.New("Post media resolver is not configured")
	}
	deliveries, err := s.files.ResolveDisplayMedia(ctx, fileIDs)
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
