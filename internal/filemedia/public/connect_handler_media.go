package public

import (
	"context"

	fileref "github.com/echovisionlab/geul-api/internal/fileref"

	"github.com/echovisionlab/geul-api/internal/mediaauth"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

func downloadResultProto(result *DownloadResult) *openv1.AuthorizeDownloadResponse {
	if result == nil {
		return nil
	}
	value := &openv1.AuthorizeDownloadResponse{Download: mediaauth.ReferenceProto(result.Download)}
	if result.Access != nil {
		value.Access = &openv1.FileDownloadAccess{Availability: openv1.FileDownloadAvailability(result.Access.Availability), Action: openv1.FileDownloadAction(result.Access.Action)}
	}
	return value
}
func (s *Service) HydrateAuthorizedContentBlockMedia(ctx context.Context, items []*contentv1.ContentBlockMediaItem) ([]*contentv1.ContentBlockMediaItem, error) {
	values := make([]*ContentMediaItem, len(items))
	for i, item := range items {
		if item == nil {
			continue
		}
		value := &ContentMediaItem{FileID: item.GetAttachment().GetActiveFileId(), DownloadAvailability: DownloadAvailability(item.DownloadAvailability), DownloadAction: DownloadAction(item.DownloadAction)}
		if selector := item.GetSelector(); selector != nil {
			value.Selector = &ContentBlockFileSelector{BlockID: selector.BlockId, ReferencePath: selector.ReferencePath}
		}
		values[i] = value
	}
	_, err := s.HydrateContentMedia(ctx, values)
	for i, value := range values {
		if value == nil || items[i] == nil || value.Selector == nil || value.FileID == "" {
			continue
		}
		item := items[i]
		item.DownloadAvailability = contentv1.ContentBlockDownloadAvailability(value.DownloadAvailability)
		item.DownloadAction = contentv1.ContentBlockDownloadAction(value.DownloadAction)
		if value.Delivery != nil {
			item.Delivery = fileref.DeliveryProto(value.Delivery)
		}
	}
	if err != nil {
		return nil, err
	}
	return items, nil
}
