package public_test

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	public "github.com/echovisionlab/geul-api/internal/filemedia/public"
	"github.com/echovisionlab/geul-api/internal/filemedia/public/publictest"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mediaauth"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestDeliveryBoundaryPreservesEveryWireField(t *testing.T) {
	filename, duration := "파일", int32(0)
	percentage := int32(0)
	downloadName := "download.webp"
	expiry := time.Unix(2000000000, 123).UTC()
	asset := &mediaasset.Reference{AssetID: "asset", URL: "https://cdn/asset", Extension: "webp", MimeType: "image/webp", FileSize: 64, SHA256: []byte{1, 2, 3}, Disposition: mediaasset.DispositionInline, DownloadFilename: &downloadName}
	inline := &mediaauth.Reference{FileID: "file", URL: "https://media/inline", Extension: "webp", MimeType: "image/webp", FileName: &filename, Purpose: mediaauth.PurposeInline, ExpiresAt: expiry}
	download := inline.Clone()
	download.URL = "https://media/download"
	download.Purpose = mediaauth.PurposeDownload
	value := &public.Delivery{ProcessingStatus: domain.ProcessingReady, ProcessingPercentage: &percentage, FileID: "file", Extension: "webp", MimeType: "image/webp", FileSize: 1024, FileName: &filename, DurationSeconds: &duration, Asset: asset, Thumbnail: asset, Spectrogram: asset, Waveform: asset, Playback: &public.HLSReference{FileID: "file", GenerationID: "generation", URL: "https://media/hls"}, Inline: inline, Download: download}
	wireAsset := &commonv1.AssetRef{AssetId: "asset", Url: "https://cdn/asset", Extension: "webp", MimeType: "image/webp", FileSize: 64, Sha256: []byte{1, 2, 3}, Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE, DownloadFilename: &downloadName}
	expected := &commonv1.MediaDelivery{ProcessingStatus: commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_READY, ProcessingPercentage: &percentage, FileId: "file", Extension: "webp", MimeType: "image/webp", FileSize: 1024, FileName: &filename, DurationSeconds: &duration, Asset: wireAsset, Thumbnail: wireAsset, Spectrogram: wireAsset, Waveform: wireAsset, Playback: &commonv1.HlsMediaRef{FileId: "file", GenerationId: "generation", Url: "https://media/hls"}, Inline: &commonv1.ExpiringMediaRef{FileId: "file", Url: "https://media/inline", Extension: "webp", MimeType: "image/webp", FileName: &filename, Purpose: commonv1.MediaDeliveryPurpose_MEDIA_DELIVERY_PURPOSE_INLINE, ExpiresAt: timestamppb.New(expiry)}, Download: &commonv1.ExpiringMediaRef{FileId: "file", Url: "https://media/download", Extension: "webp", MimeType: "image/webp", FileName: &filename, Purpose: commonv1.MediaDeliveryPurpose_MEDIA_DELIVERY_PURPOSE_DOWNLOAD, ExpiresAt: timestamppb.New(expiry)}}
	actual, err := proto.MarshalOptions{Deterministic: true}.Marshal(public.DeliveryProto(value))
	require.NoError(t, err)
	want, err := proto.MarshalOptions{Deterministic: true}.Marshal(expected)
	require.NoError(t, err)
	require.Equal(t, want, actual)
	require.Nil(t, public.DeliveryProto(nil))
	empty := public.DeliveryProto(&public.Delivery{})
	require.Nil(t, empty.FileName)
	require.Nil(t, empty.DurationSeconds)
	require.Nil(t, empty.Inline)
	clone := value.Clone()
	clone.Asset.SHA256[0] = 9
	*clone.Asset.DownloadFilename = "changed"
	*clone.ProcessingPercentage = 99
	*clone.FileName = "changed"
	*clone.DurationSeconds = 5
	clone.Inline.URL = "changed"
	*clone.Inline.FileName = "changed"
	clone.Playback.URL = "changed"
	require.Equal(t, byte(1), value.Asset.SHA256[0])
	require.Equal(t, "download.webp", *value.Asset.DownloadFilename)
	require.Zero(t, *value.ProcessingPercentage)
	require.Nil(t, empty.ProcessingPercentage)
	require.Equal(t, "파일", *value.FileName)
	require.Zero(t, *value.DurationSeconds)
	require.Equal(t, "https://media/inline", value.Inline.URL)
	require.Equal(t, "파일", *value.Inline.FileName)
	require.Equal(t, "https://media/hls", value.Playback.URL)
}
func TestContentMediaBoundaryPreservesInputAndUnknownFields(t *testing.T) {
	f := newPublicFixture(t)
	ctx := mediaasset.WithContentDownloadOwnerAuthorization(f.ctx, mediaasset.ContentDownloadOwnerAuthorization{ResourceType: f.owner.ResourceType, ResourceID: f.owner.ResourceID, DocumentID: f.owner.DocumentID, Status: f.owner.Status, Mode: mediaasset.ContentDownloadOwnerAccessPublic})
	item := &contentv1.ContentBlockMediaItem{Selector: &contentv1.ContentBlockMediaSelector{BlockId: f.relation.BlockID, ReferencePath: "file"}, Attachment: &contentv1.FileAttachment{State: &contentv1.FileAttachment_ActiveFileId{ActiveFileId: f.file.ID}}}
	unknown := []byte{0xf8, 0x3f, 0x01}
	item.ProtoReflect().SetUnknown(unknown)
	item.Attachment.ProtoReflect().SetUnknown(unknown)
	untouched := &contentv1.ContentBlockMediaItem{DownloadAction: contentv1.ContentBlockDownloadAction(99)}
	untouched.ProtoReflect().SetUnknown(unknown)
	result, err := f.service.HydrateAuthorizedContentBlockMedia(ctx, []*contentv1.ContentBlockMediaItem{item, nil, untouched})
	require.NoError(t, err)
	require.Same(t, item, result[0])
	require.Equal(t, unknown, []byte(item.ProtoReflect().GetUnknown()))
	require.Equal(t, unknown, []byte(item.Attachment.ProtoReflect().GetUnknown()))
	require.Equal(t, contentv1.ContentBlockDownloadAction_CONTENT_BLOCK_DOWNLOAD_ACTION_DOWNLOAD, item.DownloadAction)
	require.NotNil(t, item.Delivery.Download)
	require.Nil(t, result[1])
	require.Same(t, untouched, result[2])
	require.Equal(t, contentv1.ContentBlockDownloadAction(99), untouched.DownloadAction)
}

func TestContentMediaBoundaryKeepsPartialHydrationOnSigningError(t *testing.T) {
	f := newPublicFixture(t)
	asset := publictest.Asset(f.file.ID, "image", "ready", time.Now())
	f.store.Assets[asset.AssetID] = asset
	f.store.Relations[0].Audience = mediaasset.FileDownloadAudienceDisabled
	second := f.relation
	second.BlockID = uuid.NewString()
	f.store.Relations = append(f.store.Relations, second)
	f.store.BlockOwners[second.BlockID] = []public.OwnerWitness{f.owner}
	service, err := public.NewService(public.Deps{Units: f.units, SpiceDB: publicAuthorization{allowed: true}, CDNDomain: "https://cdn.example.test", MediaDomain: "https://media.example.test"})
	require.NoError(t, err)
	ctx := mediaasset.WithContentDownloadOwnerAuthorization(f.ctx, mediaasset.ContentDownloadOwnerAuthorization{ResourceType: f.owner.ResourceType, ResourceID: f.owner.ResourceID, DocumentID: f.owner.DocumentID, Status: f.owner.Status, Mode: mediaasset.ContentDownloadOwnerAccessPublic})
	items := []*contentv1.ContentBlockMediaItem{}
	for _, id := range []string{f.relation.BlockID, second.BlockID} {
		items = append(items, &contentv1.ContentBlockMediaItem{Selector: &contentv1.ContentBlockMediaSelector{BlockId: id, ReferencePath: "file"}, Attachment: &contentv1.FileAttachment{State: &contentv1.FileAttachment_ActiveFileId{ActiveFileId: f.file.ID}}})
	}
	result, err := service.HydrateAuthorizedContentBlockMedia(ctx, items)
	require.ErrorIs(t, err, mediaauth.ErrURLSigningFailed)
	require.Nil(t, result)
	require.NotNil(t, items[0].Delivery)
	require.NotNil(t, items[1].Delivery)
	require.Nil(t, items[0].Delivery.Download)
	require.Nil(t, items[1].Delivery.Download)
	require.Equal(t, contentv1.ContentBlockDownloadAction_CONTENT_BLOCK_DOWNLOAD_ACTION_NONE, items[0].DownloadAction)
}
