package public

import (
	"testing"
	"time"

	fileref "github.com/echovisionlab/geul-api/internal/fileref"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/echovisionlab/geul-api/internal/mediaauth"

	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/stretchr/testify/require"
)

func TestProjectTrackMediaDeliveryIncludesOwningReleaseDownloadRef(t *testing.T) {
	expiry := time.Unix(2000000000, 0).UTC()
	download := &mediaauth.Reference{FileID: "file-1", URL: "https://cdn.example/download", ExpiresAt: expiry}
	delivery := trackMediaDelivery(
		nil,
		TrackMedia{
			Source: MediaFile{
				ID:        "file-1",
				Extension: "wav",
				MimeType:  "audio/wav",
				FileSize:  1024,
			},
			Playback:    &fileref.HLSReference{FileID: "file-1", URL: "https://cdn.example/playback"},
			Download:    download,
			Waveform:    &mediaasset.Reference{AssetID: "waveform-1"},
			Spectrogram: &mediaasset.Reference{AssetID: "spectrogram-1"},
		},
	)
	require.Equal(t, &commonv1.ExpiringMediaRef{FileId: "file-1", Url: "https://cdn.example/download", ExpiresAt: timestamppb.New(expiry)}, delivery.Download)
}
