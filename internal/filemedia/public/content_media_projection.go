package public

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/fileref"
)

func (s *Service) HydrateAuthorizedContentBlockMedia(ctx context.Context, items []*fileref.BlockMediaItem) ([]*fileref.BlockMediaItem, error) {
	values := make([]*ContentMediaItem, len(items))
	for i, item := range items {
		if item == nil {
			continue
		}
		value := &ContentMediaItem{FileID: item.Attachment.ActiveFileID(), DownloadAvailability: DownloadAvailability(item.DownloadAvailability), DownloadAction: DownloadAction(item.DownloadAction)}
		if selector := item.Selector; selector != nil {
			value.Selector = &ContentBlockFileSelector{BlockID: selector.BlockID, ReferencePath: selector.ReferencePath}
		}
		values[i] = value
	}
	_, err := s.HydrateContentMedia(ctx, values)
	for i, value := range values {
		if value == nil || items[i] == nil || value.Selector == nil || value.FileID == "" {
			continue
		}
		item := items[i]
		item.DownloadAvailability = fileref.DownloadAvailability(value.DownloadAvailability)
		item.DownloadAction = fileref.DownloadAction(value.DownloadAction)
		if value.Delivery != nil {
			item.Delivery = value.Delivery
		}
	}
	if err != nil {
		return nil, err
	}
	return items, nil
}
