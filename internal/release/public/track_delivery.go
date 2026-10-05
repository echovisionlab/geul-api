package public

import (
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

type trackMediaDeliveryRefs struct {
	source      scopedMediaFile
	playback    *commonv1.HlsMediaRef
	download    *commonv1.ExpiringMediaRef
	waveform    *commonv1.AssetRef
	spectrogram *commonv1.AssetRef
}

func projectTrackMediaDelivery(
	durationSeconds *int,
	refs trackMediaDeliveryRefs,
) (*commonv1.MediaDelivery, error) {
	if refs.source.ID == "" || refs.playback == nil || refs.waveform == nil || refs.spectrogram == nil {
		return nil, nil
	}

	delivery := &commonv1.MediaDelivery{
		FileId:           refs.source.ID,
		Extension:        refs.source.Extension,
		MimeType:         refs.source.MimeType,
		FileSize:         refs.source.FileSize,
		FileName:         refs.source.FileName,
		Playback:         refs.playback,
		Download:         refs.download,
		Waveform:         refs.waveform,
		Spectrogram:      refs.spectrogram,
		ProcessingStatus: commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_READY,
	}
	if durationSeconds != nil {
		duration := int32(*durationSeconds)
		delivery.DurationSeconds = &duration
	}
	return delivery, nil
}
