package transcode

import (
	"testing"

	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const (
	contractFileID       = "11111111-1111-4111-8111-111111111111"
	contractGenerationID = "22222222-2222-4222-8222-222222222222"
	contractAssetID      = "33333333-3333-4333-8333-333333333333"
	contractEntityID     = "44444444-4444-4444-8444-444444444444"
	contractEventID      = "55555555-5555-4555-8555-555555555555"
)

func TestValidateCanonicalTranscodeAndWaveformEvents(t *testing.T) {
	source := canonicalContractSource(t, "ogg", "audio/ogg")
	hls := canonicalContractGeneration(t)
	spectrogram := canonicalContractAsset(t, "png", "image/png")
	waveform := canonicalContractAsset(t, "json", "application/json")

	require.NoError(t, ValidateAudioTranscodeEvent(&managev1.TranscodeAudioEvent{
		EventId: contractEventID, EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		EntityId: contractEntityID, FileId: contractFileID, Source: source,
		HlsOutput: hls, SpectrogramOutput: spectrogram,
	}))
	require.NoError(t, ValidateAudioTranscodeEvent(&managev1.TranscodeAudioEvent{
		EventId: contractEventID, EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_FILE,
		EntityId: contractFileID, FileId: contractFileID, Source: source,
		HlsOutput: hls, SpectrogramOutput: spectrogram,
	}))
	require.NoError(t, ValidateVideoTranscodeEvent(&managev1.TranscodeVideoEvent{
		EventId: contractEventID, EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
		EntityId: contractEntityID, FileId: contractFileID,
		Source: canonicalContractSource(t, "mp4", "video/mp4"), HlsOutput: hls,
		ThumbnailOutput: canonicalContractAsset(t, "webp", "image/webp"),
	}))
	require.NoError(t, ValidateWaveformGenerateEvent(&managev1.WaveformGenerateEvent{
		EventId: contractAssetID, EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK,
		EntityId: contractEntityID, FileId: contractFileID, Source: source, Output: waveform,
	}))
}

func TestValidateEventRejectsMissingContracts(t *testing.T) {
	require.EqualError(t, ValidateAudioTranscodeEvent(nil), "audio transcode event is required")
	require.EqualError(t, ValidateVideoTranscodeEvent(nil), "video transcode event is required")
	require.EqualError(t, ValidateWaveformGenerateEvent(nil), "waveform generate event is required")

	require.ErrorContains(t, ValidateAudioTranscodeEvent(&managev1.TranscodeAudioEvent{}), "audio identity")
	require.ErrorContains(t, ValidateVideoTranscodeEvent(&managev1.TranscodeVideoEvent{}), "video identity")
	require.ErrorContains(t, ValidateWaveformGenerateEvent(&managev1.WaveformGenerateEvent{}), "waveform identity")

	source := canonicalContractSource(t, "ogg", "audio/ogg")
	hls := canonicalContractGeneration(t)
	waveform := canonicalContractAsset(t, "json", "application/json")
	require.ErrorContains(t, ValidateAudioTranscodeEvent(&managev1.TranscodeAudioEvent{
		EventId: contractEventID, EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		EntityId: contractEntityID, FileId: contractFileID,
	}), "audio source")
	require.ErrorContains(t, ValidateAudioTranscodeEvent(&managev1.TranscodeAudioEvent{
		EventId: contractEventID, EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		EntityId: contractEntityID, FileId: contractFileID, Source: source,
	}), "audio HLS output")
	require.ErrorContains(t, ValidateAudioTranscodeEvent(&managev1.TranscodeAudioEvent{
		EventId: contractEventID, EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		EntityId: contractEntityID, FileId: contractFileID, Source: source, HlsOutput: hls,
	}), "audio spectrogram output")
	require.ErrorContains(t, ValidateVideoTranscodeEvent(&managev1.TranscodeVideoEvent{
		EventId: contractEventID, EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_WORK,
		EntityId: contractEntityID, FileId: contractFileID,
	}), "video source")
	require.ErrorContains(t, ValidateVideoTranscodeEvent(&managev1.TranscodeVideoEvent{
		EventId: contractEventID, EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_WORK,
		EntityId: contractEntityID, FileId: contractFileID, Source: canonicalContractSource(t, "mp4", "video/mp4"),
	}), "video HLS output")
	require.ErrorContains(t, ValidateVideoTranscodeEvent(&managev1.TranscodeVideoEvent{
		EventId: contractEventID, EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_WORK,
		EntityId: contractEntityID, FileId: contractFileID,
		Source: canonicalContractSource(t, "mp4", "video/mp4"), HlsOutput: hls,
	}), "video thumbnail output")
	require.ErrorContains(t, ValidateWaveformGenerateEvent(&managev1.WaveformGenerateEvent{
		EventId: contractAssetID, EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK,
		EntityId: contractEntityID, FileId: contractFileID,
	}), "waveform source")
	require.ErrorContains(t, ValidateWaveformGenerateEvent(&managev1.WaveformGenerateEvent{
		EventId: contractAssetID, EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK,
		EntityId: contractEntityID, FileId: contractFileID, Source: source,
	}), "waveform output")
	require.ErrorContains(t, ValidateWaveformGenerateEvent(&managev1.WaveformGenerateEvent{
		EventId: contractGenerationID, EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK,
		EntityId: contractEntityID, FileId: contractFileID, Source: source, Output: waveform,
	}), "event_id must match")
}

func TestValidateJobIdentityRejectsNonCanonicalValues(t *testing.T) {
	require.ErrorContains(t, validateJobIdentity("invalid", managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST, contractEntityID, contractFileID), "event_id")
	require.ErrorContains(t, validateJobIdentity(contractEventID, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST, "invalid", contractFileID), "entity_id")
	require.ErrorContains(t, validateJobIdentity(contractEventID, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST, contractEntityID, "invalid"), "file_id")
	require.ErrorContains(t, validateJobIdentity(contractEventID, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED, contractEntityID, contractFileID), "unsupported entity_type")
}

func TestValidateMediaObjectTargetRejectsNonCanonicalMetadata(t *testing.T) {
	valid := canonicalContractSource(t, "ogg", "audio/ogg")
	tests := []struct {
		name   string
		fileID string
		mutate func(*commonv1.MediaObjectTarget)
		want   string
	}{
		{name: "file mismatch", fileID: contractGenerationID, want: "file_id does not match"},
		{name: "uppercase extension", fileID: contractFileID, mutate: func(target *commonv1.MediaObjectTarget) { target.Extension = "OGG" }, want: "extension is not canonical"},
		{name: "parameterized mime", fileID: contractFileID, mutate: func(target *commonv1.MediaObjectTarget) { target.MimeType = "audio/ogg; charset=binary" }, want: "mime_type is not canonical"},
		{name: "mime mismatch", fileID: contractFileID, mutate: func(target *commonv1.MediaObjectTarget) { target.MimeType = "audio/mpeg" }, want: "extension does not match"},
		{name: "invalid file id", fileID: "invalid", mutate: func(target *commonv1.MediaObjectTarget) { target.FileId = "invalid" }, want: "invalid file_id"},
		{name: "arbitrary key", fileID: contractFileID, mutate: func(target *commonv1.MediaObjectTarget) { target.ObjectKey = "legacy/audio.ogg" }, want: "object_key is not canonical"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := proto.Clone(valid).(*commonv1.MediaObjectTarget)
			if tt.mutate != nil {
				tt.mutate(target)
			}
			require.ErrorContains(t, validateMediaObjectTarget(tt.fileID, target), tt.want)
		})
	}
	require.ErrorContains(t, validateMediaObjectTarget(contractFileID, nil), "target is required")
}

func TestValidateMediaGenerationTargetRejectsArbitraryPrefix(t *testing.T) {
	valid := canonicalContractGeneration(t)
	require.ErrorContains(t, validateMediaGenerationTarget(contractFileID, nil), "target is required")

	wrongFile := proto.Clone(valid).(*commonv1.MediaGenerationWriteTarget)
	wrongFile.FileId = contractGenerationID
	require.ErrorContains(t, validateMediaGenerationTarget(contractFileID, wrongFile), "file_id does not match")

	invalidGeneration := proto.Clone(valid).(*commonv1.MediaGenerationWriteTarget)
	invalidGeneration.GenerationId = "invalid"
	require.ErrorContains(t, validateMediaGenerationTarget(contractFileID, invalidGeneration), "invalid file_id or generation_id")

	arbitraryPrefix := proto.Clone(valid).(*commonv1.MediaGenerationWriteTarget)
	arbitraryPrefix.ObjectPrefix = "legacy/hls"
	require.ErrorContains(t, validateMediaGenerationTarget(contractFileID, arbitraryPrefix), "object_prefix is not canonical")
}

func TestValidateAssetTargetRejectsArbitraryMetadata(t *testing.T) {
	valid := canonicalContractAsset(t, "json", "application/json")
	require.ErrorContains(t, validateAssetTarget(nil, "json", "application/json"), "target is required")

	wrongMetadata := proto.Clone(valid).(*commonv1.AssetWriteTarget)
	wrongMetadata.Extension = "png"
	require.ErrorContains(t, validateAssetTarget(wrongMetadata, "json", "application/json"), "extension or mime_type")

	attachment := proto.Clone(valid).(*commonv1.AssetWriteTarget)
	attachment.Disposition = commonv1.AssetDisposition_ASSET_DISPOSITION_ATTACHMENT
	require.ErrorContains(t, validateAssetTarget(attachment, "json", "application/json"), "inline disposition")

	filename := "waveform.json"
	withFilename := proto.Clone(valid).(*commonv1.AssetWriteTarget)
	withFilename.DownloadFilename = &filename
	require.ErrorContains(t, validateAssetTarget(withFilename, "json", "application/json"), "inline disposition")

	invalidAsset := proto.Clone(valid).(*commonv1.AssetWriteTarget)
	invalidAsset.AssetId = "invalid"
	require.ErrorContains(t, validateAssetTarget(invalidAsset, "json", "application/json"), "invalid asset_id")

	arbitraryKey := proto.Clone(valid).(*commonv1.AssetWriteTarget)
	arbitraryKey.ObjectKey = "legacy/waveform.json"
	require.ErrorContains(t, validateAssetTarget(arbitraryKey, "json", "application/json"), "object_key is not canonical")
}

func TestMatchTranscodeCompletionPayload(t *testing.T) {
	audioJob := &managev1.TranscodeAudioEvent{
		EventId: contractEventID, EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		EntityId: contractEntityID, FileId: contractFileID,
		Source: canonicalContractSource(t, "ogg", "audio/ogg"), HlsOutput: canonicalContractGeneration(t),
		SpectrogramOutput: canonicalContractAsset(t, "png", "image/png"),
	}
	audioPayload, err := proto.Marshal(audioJob)
	require.NoError(t, err)
	audioCompletion := &managev1.TranscodeCompleteEvent{
		EventType:  managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO,
		EntityType: audioJob.EntityType, EntityId: audioJob.EntityId, FileId: audioJob.FileId,
		Outputs: &managev1.TranscodeOutputs{
			Hls:         &commonv1.MediaGenerationWriteResult{GenerationId: contractGenerationID},
			Spectrogram: &commonv1.AssetWriteResult{AssetId: contractAssetID},
		},
	}
	matched, err := MatchTranscodeCompletionPayload(audioCompletion, audioPayload)
	require.NoError(t, err)
	require.True(t, matched)

	audioCompletion.EntityId = contractFileID
	matched, err = MatchTranscodeCompletionPayload(audioCompletion, audioPayload)
	require.NoError(t, err)
	require.False(t, matched)
	audioCompletion.EntityId = contractEntityID
	audioCompletion.Outputs.Spectrogram.AssetId = contractGenerationID
	matched, err = MatchTranscodeCompletionPayload(audioCompletion, audioPayload)
	require.NoError(t, err)
	require.False(t, matched)

	videoAssetID := "66666666-6666-4666-8666-666666666666"
	videoJob := &managev1.TranscodeVideoEvent{
		EventId: contractEventID, EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE,
		EntityId: contractEntityID, FileId: contractFileID,
		Source: canonicalContractSource(t, "mp4", "video/mp4"), HlsOutput: canonicalContractGeneration(t),
		ThumbnailOutput: canonicalContractAssetWithID(t, videoAssetID, "webp", "image/webp"),
	}
	videoPayload, err := proto.Marshal(videoJob)
	require.NoError(t, err)
	videoCompletion := &managev1.TranscodeCompleteEvent{
		EventType:  managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_VIDEO,
		EntityType: videoJob.EntityType, EntityId: videoJob.EntityId, FileId: videoJob.FileId,
		Outputs: &managev1.TranscodeOutputs{
			Hls:       &commonv1.MediaGenerationWriteResult{GenerationId: contractGenerationID},
			Thumbnail: &commonv1.AssetWriteResult{AssetId: videoAssetID},
		},
	}
	matched, err = MatchTranscodeCompletionPayload(videoCompletion, videoPayload)
	require.NoError(t, err)
	require.True(t, matched)
	videoCompletion.FileId = contractGenerationID
	matched, err = MatchTranscodeCompletionPayload(videoCompletion, videoPayload)
	require.NoError(t, err)
	require.False(t, matched)
}

func TestMatchTranscodeCompletionPayloadRejectsInvalidContracts(t *testing.T) {
	matched, err := MatchTranscodeCompletionPayload(nil, nil)
	require.NoError(t, err)
	require.False(t, matched)
	matched, err = MatchTranscodeCompletionPayload(&managev1.TranscodeCompleteEvent{}, nil)
	require.NoError(t, err)
	require.False(t, matched)

	for _, eventType := range []managev1.TranscodeEventType{
		managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO,
		managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_VIDEO,
	} {
		_, err = MatchTranscodeCompletionPayload(&managev1.TranscodeCompleteEvent{
			EventType: eventType, Outputs: &managev1.TranscodeOutputs{},
		}, []byte{0xff})
		require.Error(t, err)
	}

	invalidAudio, err := proto.Marshal(&managev1.TranscodeAudioEvent{})
	require.NoError(t, err)
	_, err = MatchTranscodeCompletionPayload(&managev1.TranscodeCompleteEvent{
		EventType: managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO,
		Outputs:   &managev1.TranscodeOutputs{},
	}, invalidAudio)
	require.ErrorContains(t, err, "validate tracked audio")
	invalidVideo, err := proto.Marshal(&managev1.TranscodeVideoEvent{})
	require.NoError(t, err)
	_, err = MatchTranscodeCompletionPayload(&managev1.TranscodeCompleteEvent{
		EventType: managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_VIDEO,
		Outputs:   &managev1.TranscodeOutputs{},
	}, invalidVideo)
	require.ErrorContains(t, err, "validate tracked video")

	matched, err = MatchTranscodeCompletionPayload(&managev1.TranscodeCompleteEvent{
		EventType: managev1.TranscodeEventType_TRANSCODE_EVENT_TYPE_UNSPECIFIED,
		Outputs:   &managev1.TranscodeOutputs{},
	}, nil)
	require.NoError(t, err)
	require.False(t, matched)
}

func canonicalContractSource(t *testing.T, extension string, mimeType string) *commonv1.MediaObjectTarget {
	t.Helper()
	objectKey, err := mediaauth.MediaObjectKey(contractFileID, extension)
	require.NoError(t, err)
	return &commonv1.MediaObjectTarget{
		FileId: contractFileID, ObjectKey: objectKey, Extension: extension, MimeType: mimeType,
	}
}

func canonicalContractGeneration(t *testing.T) *commonv1.MediaGenerationWriteTarget {
	t.Helper()
	objectPrefix, err := mediaauth.MediaHLSObjectPrefix(contractFileID, contractGenerationID)
	require.NoError(t, err)
	return &commonv1.MediaGenerationWriteTarget{
		GenerationId: contractGenerationID, FileId: contractFileID, ObjectPrefix: objectPrefix,
	}
}

func canonicalContractAsset(t *testing.T, extension string, mimeType string) *commonv1.AssetWriteTarget {
	return canonicalContractAssetWithID(t, contractAssetID, extension, mimeType)
}

func canonicalContractAssetWithID(t *testing.T, assetID string, extension string, mimeType string) *commonv1.AssetWriteTarget {
	t.Helper()
	objectKey, err := mediaauth.AssetObjectKey(assetID, extension)
	require.NoError(t, err)
	return &commonv1.AssetWriteTarget{
		AssetId: assetID, ObjectKey: objectKey, Extension: extension, MimeType: mimeType,
		Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	}
}
