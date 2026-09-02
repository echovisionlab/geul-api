package post

import (
	"context"
	"strings"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"google.golang.org/protobuf/proto"
)

type featuredImageIdentity[T any] func(T) (entityID string, fileID *string)
type featuredImageDeliveryResolver func(context.Context, []string) (map[string]*commonv1.MediaDelivery, error)

func loadFeaturedImageDeliveries[T any](
	ctx context.Context,
	entities []T,
	identity featuredImageIdentity[T],
	resolve featuredImageDeliveryResolver,
) (map[string]*commonv1.MediaDelivery, error) {
	result := make(map[string]*commonv1.MediaDelivery, len(entities))
	fileIDs := make([]string, 0, len(entities))
	entityIDsByFileID := make(map[string][]string, len(entities))
	for _, entity := range entities {
		entityID, fileIDPointer := identity(entity)
		if fileIDPointer == nil {
			continue
		}
		fileID := strings.TrimSpace(*fileIDPointer)
		if fileID == "" {
			continue
		}
		if _, exists := entityIDsByFileID[fileID]; !exists {
			fileIDs = append(fileIDs, fileID)
		}
		entityIDsByFileID[fileID] = append(entityIDsByFileID[fileID], entityID)
	}
	if len(fileIDs) == 0 {
		return result, nil
	}
	deliveries, err := resolve(ctx, fileIDs)
	if err != nil {
		return nil, err
	}
	for fileID, entityIDs := range entityIDsByFileID {
		if delivery := deliveries[fileID]; delivery != nil {
			for _, entityID := range entityIDs {
				result[entityID] = delivery
			}
		}
	}
	return result, nil
}

func postPublicDisplayOnly(delivery *commonv1.MediaDelivery) *commonv1.MediaDelivery {
	if delivery == nil {
		return nil
	}
	result := proto.Clone(delivery).(*commonv1.MediaDelivery)
	result.Inline = nil
	result.Download = nil
	return result
}

func (s *PostService) loadPostPublicFeaturedImageDeliveries(
	ctx context.Context,
	posts []model.Post,
) (map[string]*commonv1.MediaDelivery, error) {
	deliveries, err := loadFeaturedImageDeliveries(ctx, posts, func(post model.Post) (string, *string) {
		return post.ID, post.FeaturedImageFileID
	}, s.fileService.ResolvePublicDisplayMedia)
	if err != nil {
		return nil, err
	}
	for postID, delivery := range deliveries {
		deliveries[postID] = postPublicDisplayOnly(delivery)
	}
	return deliveries, nil
}

func (s *PostService) resolveAuthorizedPostFeaturedImage(
	ctx context.Context,
	post *model.Post,
) (*commonv1.MediaDelivery, error) {
	expectedFileID := ""
	if post != nil && post.FeaturedImageFileID != nil {
		expectedFileID = strings.TrimSpace(*post.FeaturedImageFileID)
	}
	postID := ""
	if post != nil {
		postID = post.ID
	}
	return s.fileService.ResolveAuthorizedPostFeaturedImage(ctx, postID, expectedFileID)
}
