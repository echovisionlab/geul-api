package public

import (
	"testing"

	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

func TestProjectTrackMediaDeliveryIncludesOwningReleaseDownloadRef(t *testing.T) {
	download := &commonv1.ExpiringMediaRef{FileId: "file-1", Url: "https://cdn.example/download"}
	delivery := trackMediaDelivery(
		nil,
		TrackMedia{
			Source: MediaFile{
				ID:        "file-1",
				Extension: "wav",
				MimeType:  "audio/wav",
				FileSize:  1024,
			},
			Playback:    &commonv1.HlsMediaRef{FileId: "file-1", Url: "https://cdn.example/playback"},
			Download:    download,
			Waveform:    &mediaasset.Reference{AssetID: "waveform-1"},
			Spectrogram: &mediaasset.Reference{AssetID: "spectrogram-1"},
		},
	)
	require.Same(t, download, delivery.GetDownload())
}
