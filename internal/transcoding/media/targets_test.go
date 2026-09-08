package media

import (
	"testing"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestValidateIdentity(t *testing.T) {
	t.Parallel()
	eventID, entityID, fileID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	require.NoError(t, ValidateIdentity(eventID, apiv1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST, entityID, fileID))
	require.NoError(t, ValidateIdentity(eventID, apiv1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_FILE, fileID, fileID))
	require.ErrorContains(t, ValidateIdentity("bad", apiv1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST, entityID, fileID), "event_id")
	require.ErrorContains(t, ValidateIdentity(eventID, apiv1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED, entityID, fileID), "unsupported")
}

func TestValidateSource(t *testing.T) {
	t.Parallel()
	fileID := uuid.NewString()
	valid := func() *commonv1.MediaObjectTarget {
		key, err := mediaauth.MediaObjectKey(fileID, "mp3")
		require.NoError(t, err)
		return &commonv1.MediaObjectTarget{
			FileId: fileID, ObjectKey: key, Extension: "mp3", MimeType: "audio/mpeg",
		}
	}
	require.NoError(t, ValidateSource(valid(), fileID, "audio"))
	require.ErrorContains(t, ValidateSource(nil, fileID, "audio"), "required")

	tests := []struct {
		name   string
		mutate func(*commonv1.MediaObjectTarget)
		want   string
	}{
		{name: "file", mutate: func(target *commonv1.MediaObjectTarget) { target.FileId = uuid.NewString() }, want: "does not match"},
		{name: "noncanonical extension", mutate: func(target *commonv1.MediaObjectTarget) { target.Extension = "MP3" }, want: "not canonical"},
		{name: "invalid extension", mutate: func(target *commonv1.MediaObjectTarget) { target.Extension = "bad/path" }, want: "not canonical"},
		{name: "noncanonical MIME", mutate: func(target *commonv1.MediaObjectTarget) { target.MimeType = "audio/mpeg; x=y" }, want: "mime_type"},
		{name: "wrong media kind", mutate: func(target *commonv1.MediaObjectTarget) { target.MimeType = "video/mp4" }, want: "must start"},
		{name: "extension mismatch", mutate: func(target *commonv1.MediaObjectTarget) { target.MimeType = "audio/ogg" }, want: "does not match"},
		{name: "object key", mutate: func(target *commonv1.MediaObjectTarget) { target.ObjectKey = "wrong" }, want: "object_key"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target := valid()
			tc.mutate(target)
			require.ErrorContains(t, ValidateSource(target, fileID, "audio"), tc.want)
		})
	}
}

func TestValidateOutputs(t *testing.T) {
	t.Parallel()
	fileID, generationID, assetID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	prefix, err := mediaauth.MediaHLSObjectPrefix(fileID, generationID)
	require.NoError(t, err)
	hlsTarget := &commonv1.MediaGenerationWriteTarget{FileId: fileID, GenerationId: generationID, ObjectPrefix: prefix}
	require.NoError(t, ValidateHLSOutput(hlsTarget, fileID))
	require.ErrorContains(t, ValidateHLSOutput(nil, fileID), "required")
	wrongFile := proto.Clone(hlsTarget).(*commonv1.MediaGenerationWriteTarget)
	wrongFile.FileId = uuid.NewString()
	require.ErrorContains(t, ValidateHLSOutput(wrongFile, fileID), "does not match")
	badGeneration := proto.Clone(hlsTarget).(*commonv1.MediaGenerationWriteTarget)
	badGeneration.GenerationId = "bad"
	require.Error(t, ValidateHLSOutput(badGeneration, fileID))
	wrongPrefix := proto.Clone(hlsTarget).(*commonv1.MediaGenerationWriteTarget)
	wrongPrefix.ObjectPrefix = "wrong"
	require.ErrorContains(t, ValidateHLSOutput(wrongPrefix, fileID), "object_prefix")

	key, err := mediaauth.AssetObjectKey(assetID, "json")
	require.NoError(t, err)
	asset := &commonv1.AssetWriteTarget{
		AssetId: assetID, ObjectKey: key, Extension: "json", MimeType: "application/json",
		Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	}
	require.NoError(t, ValidateAssetOutput(asset, "json", "application/json"))
	require.ErrorContains(t, ValidateAssetOutput(nil, "json", "application/json"), "required")

	for _, tc := range []struct {
		name   string
		mutate func(*commonv1.AssetWriteTarget)
		want   string
	}{
		{name: "extension", mutate: func(target *commonv1.AssetWriteTarget) { target.Extension = "txt" }, want: "extension"},
		{name: "MIME", mutate: func(target *commonv1.AssetWriteTarget) { target.MimeType = "text/plain" }, want: "mime_type"},
		{name: "disposition", mutate: func(target *commonv1.AssetWriteTarget) {
			target.Disposition = commonv1.AssetDisposition_ASSET_DISPOSITION_ATTACHMENT
		}, want: "inline"},
		{name: "asset ID", mutate: func(target *commonv1.AssetWriteTarget) { target.AssetId = "bad" }, want: "invalid media path"},
		{name: "object key", mutate: func(target *commonv1.AssetWriteTarget) { target.ObjectKey = "wrong" }, want: "object_key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := proto.Clone(asset).(*commonv1.AssetWriteTarget)
			tc.mutate(target)
			require.ErrorContains(t, ValidateAssetOutput(target, "json", "application/json"), tc.want)
		})
	}
}
