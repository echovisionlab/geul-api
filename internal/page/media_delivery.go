package page

import (
	"context"
	"strings"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

func (s *PageService) loadPageFeaturedImageDeliveries(
	ctx context.Context,
	pages []model.Page,
) (map[string]*commonv1.MediaDelivery, error) {
	resolver, ok := s.fileService.(FileDeliveryResolver)
	if !ok {
		return nil, errs.InternalMsg("Page file delivery resolver is not configured")
	}
	result := make(map[string]*commonv1.MediaDelivery, len(pages))
	for _, page := range pages {
		expectedFileID := ""
		if page.FeaturedImageFileID != nil {
			expectedFileID = strings.TrimSpace(*page.FeaturedImageFileID)
		}
		delivery, err := resolver.ResolveAuthorizedPageFeaturedImage(ctx, page.ID, expectedFileID)
		if err != nil {
			return nil, err
		}
		if delivery != nil {
			result[page.ID] = delivery
		}
	}
	return result, nil
}
