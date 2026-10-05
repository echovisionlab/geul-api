package public

import (
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

func TestProjectTrackMediaDeliveryIncludesOwningReleaseDownloadRef(t *testing.T) {
	download := &commonv1.ExpiringMediaRef{FileId: "file-1", Url: "https://cdn.example/download"}
	delivery, err := projectTrackMediaDelivery(
		nil,
		trackMediaDeliveryRefs{
			source: scopedMediaFile{
				ID:        "file-1",
				Extension: "wav",
				MimeType:  "audio/wav",
				FileSize:  1024,
			},
			playback:    &commonv1.HlsMediaRef{FileId: "file-1", Url: "https://cdn.example/playback"},
			download:    download,
			waveform:    &commonv1.AssetRef{AssetId: "waveform-1"},
			spectrogram: &commonv1.AssetRef{AssetId: "spectrogram-1"},
		},
	)
	require.NoError(t, err)
	require.Same(t, download, delivery.GetDownload())
}
