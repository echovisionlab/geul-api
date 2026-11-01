package public_test

import (
	"testing"
	"time"

	. "github.com/echovisionlab/geul-api/internal/filemedia/public"

	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadyPublicAssetProjectionRejectsIncompleteMetadataUnit(t *testing.T) {
	base := ReadyAsset{
		AssetID:     uuid.NewString(),
		Kind:        "image",
		Extension:   "webp",
		MimeType:    "image/webp",
		FileSize:    1,
		SHA256:      make([]byte, 32),
		Disposition: "inline",
	}

	for name, mutate := range map[string]func(*ReadyAsset){
		"missing id":        func(row *ReadyAsset) { row.AssetID = "" },
		"missing extension": func(row *ReadyAsset) { row.Extension = "" },
		"missing mime":      func(row *ReadyAsset) { row.MimeType = "" },
		"missing size":      func(row *ReadyAsset) { row.FileSize = 0 },
		"invalid digest":    func(row *ReadyAsset) { row.SHA256 = []byte{1} },
		"mime mismatch":     func(row *ReadyAsset) { row.Extension = "png" },
		"bad disposition":   func(row *ReadyAsset) { row.Disposition = "public" },
		"missing filename": func(row *ReadyAsset) {
			row.Disposition = "attachment"
			row.DownloadFilename = nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			row := base
			mutate(&row)
			_, err := ExportProjectReadyPublicAsset("https://cdn.example.com", row)
			require.Error(t, err)
		})
	}

	attachment := base
	attachment.Disposition = "attachment"
	filename := "asset.webp"
	attachment.DownloadFilename = &filename
	_, err := ExportProjectReadyPublicAsset("", attachment)
	require.ErrorContains(t, err, "cannot be emitted")
}

func TestCanonicalExpiringMediaRefsUsePurposePolicyUnit(t *testing.T) {
	fileID := uuid.NewString()
	before := time.Now().UTC()
	downloadTTL := 3 * time.Minute
	download, err := mediaauth.ExpiringFileRef(
		"https://media.example.com",
		"media-secret",
		fileID,
		"flac",
		"audio/flac",
		nil,
		mediaauth.PurposeDownload,
		downloadTTL,
	)
	require.NoError(t, err)
	assert.Equal(t, commonv1.MediaDeliveryPurpose_MEDIA_DELIVERY_PURPOSE_DOWNLOAD, download.GetPurpose())
	assert.Contains(t, download.GetUrl(), "/media/")
	assert.WithinDuration(t, before.Add(downloadTTL), download.GetExpiresAt().AsTime(), 2*time.Second)

	generationID := uuid.NewString()
	playback, err := ExportBuildPublicHLSRef(
		"https://media.example.com",
		ReadyGeneration{FileID: fileID, GenerationID: generationID, ManifestName: "master.m3u8"},
	)
	require.NoError(t, err)
	assert.Equal(t, generationID, playback.GetGenerationId())
	assert.Equal(t, "https://media.example.com/media/"+fileID+"/hls/"+generationID+"/master.m3u8", playback.GetUrl())
	assert.Equal(t, commonv1.MediaDeliveryPurpose_MEDIA_DELIVERY_PURPOSE_UNSPECIFIED, mediaauth.DeliveryPurpose(mediaauth.Purpose("unknown")))
	assert.Equal(t, "/asset/id/image.webp", ExportJoinPublicCDNPath("", "/asset/id/image.webp"))
	assert.Equal(t, "https://cdn.example.com/asset/id/image.webp", ExportJoinPublicCDNPath("cdn.example.com/", "asset/id/image.webp"))
}
