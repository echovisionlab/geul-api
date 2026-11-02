package post

import (
	"context"
	"strings"

	fileref "github.com/echovisionlab/geul-api/internal/fileref"
)

type featuredImageIdentity[T any] func(T) (entityID string, fileID *string)
type featuredImageDeliveryResolver func(context.Context, []string) (map[string]*fileref.Delivery, error)

func loadFeaturedImageDeliveries[T any](
	ctx context.Context,
	entities []T,
	identity featuredImageIdentity[T],
	resolve featuredImageDeliveryResolver,
) (map[string]*fileref.Delivery, error) {
	result := make(map[string]*fileref.Delivery, len(entities))
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

func postPublicDisplayOnly(delivery *fileref.Delivery) *fileref.Delivery {
	if delivery == nil {
		return nil
	}
	result := delivery.Clone()
	result.Inline = nil
	result.Download = nil
	return result
}

func (s *Service) loadPostPublicFeaturedImageDeliveries(
	ctx context.Context,
	posts []Post,
) (map[string]*fileref.Delivery, error) {
	deliveries, err := loadFeaturedImageDeliveries(ctx, posts, func(listed Post) (string, *string) {
		return listed.ID, listed.FeaturedImageFileID
	}, s.fileService.ResolveDisplayMedia)
	if err != nil {
		return nil, err
	}
	for postID, delivery := range deliveries {
		deliveries[postID] = postPublicDisplayOnly(delivery)
	}
	return deliveries, nil
}

func (s *Service) resolveAuthorizedPostFeaturedImage(
	ctx context.Context,
	stored *Post,
) (*fileref.Delivery, error) {
	expectedFileID := ""
	if stored != nil && stored.FeaturedImageFileID != nil {
		expectedFileID = strings.TrimSpace(*stored.FeaturedImageFileID)
	}
	postID := ""
	if stored != nil {
		postID = stored.ID
	}
	return s.fileService.ResolveAuthorizedPostFeaturedImage(ctx, postID, expectedFileID)
}
