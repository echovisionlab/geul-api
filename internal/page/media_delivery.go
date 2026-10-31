package page

import (
	"context"
	"strings"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

func (s *Service) loadPageFeaturedImageDeliveries(
	ctx context.Context,
	pages []model.Page,
) (map[string]*commonv1.MediaDelivery, error) {
	result := make(map[string]*commonv1.MediaDelivery, len(pages))
	for _, page := range pages {
		expectedFileID := ""
		if page.FeaturedImageFileID != nil {
			expectedFileID = strings.TrimSpace(*page.FeaturedImageFileID)
		}
		delivery, err := s.files.ResolveAuthorizedPageFeaturedImage(ctx, page.ID, expectedFileID)
		if err != nil {
			return nil, err
		}
		if delivery != nil {
			result[page.ID] = delivery
		}
	}
	return result, nil
}
