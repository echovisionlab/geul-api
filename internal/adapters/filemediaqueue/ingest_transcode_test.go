package filemediaqueue

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestIngestCommandMappingPreservesEveryProtocolTarget(t *testing.T) {
	input := domain.IngestTranscodeCommand{EventID: "event", FileID: "file", EntityID: "owner", EntityType: int32(managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK), Source: &domain.ObjectTarget{FileID: "file", ObjectKey: "media/file.mp4", Extension: "mp4", MimeType: "audio/mp4"}, HLSOutput: &domain.GenerationTarget{GenerationID: "generation", FileID: "file", ObjectPrefix: "media/file/hls/generation"}, ArtifactOutput: &mediaasset.WriteTarget{AssetID: "asset", ObjectKey: "asset/asset.png", Extension: "png", MimeType: "image/png", Disposition: mediaasset.DispositionInline, DownloadFilename: new("source.png")}}
	source := &commonv1.MediaObjectTarget{FileId: "file", ObjectKey: "media/file.mp4", Extension: "mp4", MimeType: "audio/mp4"}
	hls := &commonv1.MediaGenerationWriteTarget{GenerationId: "generation", FileId: "file", ObjectPrefix: "media/file/hls/generation"}
	asset := &commonv1.AssetWriteTarget{AssetId: "asset", ObjectKey: "asset/asset.png", Extension: "png", MimeType: "image/png", Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE, DownloadFilename: new("source.png")}
	audio := &managev1.TranscodeAudioEvent{EventId: "event", FileId: "file", EntityId: "owner", EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK, Source: source, HlsOutput: hls, SpectrogramOutput: asset}
	video := &managev1.TranscodeVideoEvent{EventId: "event", FileId: "file", EntityId: "owner", EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK, Source: source, HlsOutput: hls, ThumbnailOutput: asset}
	require.True(t, proto.Equal(audio, AudioIngestEvent(input)))
	require.True(t, proto.Equal(video, VideoIngestEvent(input)))
	input.Source, input.HLSOutput, input.ArtifactOutput = nil, nil, nil
	input.EntityType = 999
	got := AudioIngestEvent(input)
	require.Nil(t, got.Source)
	require.Nil(t, got.HlsOutput)
	require.Nil(t, got.SpectrogramOutput)
	require.EqualValues(t, 999, got.EntityType)
}
