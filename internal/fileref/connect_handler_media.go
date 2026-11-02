package fileref

import (
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mediaauth"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

func HLSReferenceProto(ref *HLSReference) *commonv1.HlsMediaRef {
	if ref == nil {
		return nil
	}
	return &commonv1.HlsMediaRef{FileId: ref.FileID, GenerationId: ref.GenerationID, Url: ref.URL}
}
func DeliveryProto(d *Delivery) *commonv1.MediaDelivery {
	if d == nil {
		return nil
	}
	return &commonv1.MediaDelivery{FileId: d.FileID, Extension: d.Extension, MimeType: d.MimeType, FileSize: d.FileSize, ProcessingStatus: commonv1.MediaProcessingStatus(d.ProcessingStatus), ProcessingPercentage: d.ProcessingPercentage, FileName: d.FileName, DurationSeconds: d.DurationSeconds, Asset: mediaasset.ReferenceProto(d.Asset), Thumbnail: mediaasset.ReferenceProto(d.Thumbnail), Spectrogram: mediaasset.ReferenceProto(d.Spectrogram), Waveform: mediaasset.ReferenceProto(d.Waveform), Playback: HLSReferenceProto(d.Playback), Inline: mediaauth.ReferenceProto(d.Inline), Download: mediaauth.ReferenceProto(d.Download)}
}
