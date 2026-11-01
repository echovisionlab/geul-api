package public

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mediaauth"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

func hlsReferenceProto(ref *HLSReference) *commonv1.HlsMediaRef {
	if ref == nil {
		return nil
	}
	return &commonv1.HlsMediaRef{FileId: ref.FileID, GenerationId: ref.GenerationID, Url: ref.URL}
}
func DeliveryProto(d *Delivery) *commonv1.MediaDelivery {
	if d == nil {
		return nil
	}
	return &commonv1.MediaDelivery{FileId: d.FileID, Extension: d.Extension, MimeType: d.MimeType, FileSize: d.FileSize, ProcessingStatus: commonv1.MediaProcessingStatus(d.ProcessingStatus), ProcessingPercentage: d.ProcessingPercentage, FileName: d.FileName, DurationSeconds: d.DurationSeconds, Asset: mediaasset.ReferenceProto(d.Asset), Thumbnail: mediaasset.ReferenceProto(d.Thumbnail), Spectrogram: mediaasset.ReferenceProto(d.Spectrogram), Waveform: mediaasset.ReferenceProto(d.Waveform), Playback: hlsReferenceProto(d.Playback), Inline: mediaauth.ReferenceProto(d.Inline), Download: mediaauth.ReferenceProto(d.Download)}
}
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
func (s *Service) ResolveReadyOGAsset(ctx context.Context, source, localized *string) (*commonv1.AssetRef, error) {
	ref, err := s.ResolveOGAsset(ctx, source, localized)
	return mediaasset.ReferenceProto(ref), err
}
func (s *Service) ResolvePublicDisplayMedia(ctx context.Context, ids []string) (map[string]*commonv1.MediaDelivery, error) {
	values, err := s.ResolveDisplayMedia(ctx, ids)
	if values == nil {
		return nil, err
	}
	result := make(map[string]*commonv1.MediaDelivery, len(values))
	for id, value := range values {
		result[id] = DeliveryProto(value)
	}
	return result, err
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
			item.Delivery = DeliveryProto(value.Delivery)
		}
	}
	if err != nil {
		return nil, err
	}
	return items, nil
}
