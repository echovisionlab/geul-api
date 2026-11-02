package fileref

import (
	"testing"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestBlockMediaCodecPreservesAttachmentStateAndExtensions(t *testing.T) {
	unknown := []byte{0xf8, 0x3f, 0x01}
	missing := &MissingAttachment{FormerFileID: "deleted", MediaKind: MissingMediaKind(-7), Extensions: unknown}
	item := &BlockMediaItem{Selector: &BlockMediaSelector{BlockID: "block", ReferencePath: "file", Extensions: unknown}, Attachment: &Attachment{Kind: AttachmentMissing, Missing: missing, Extensions: unknown}, DownloadAvailability: DownloadAvailability(-1), DownloadAction: DownloadAction(99), Extensions: unknown}
	expected := &contentv1.ContentBlockMediaItem{Selector: &contentv1.ContentBlockMediaSelector{BlockId: "block", ReferencePath: "file"}, Attachment: &contentv1.FileAttachment{State: &contentv1.FileAttachment_MissingAttachment{MissingAttachment: &contentv1.MissingAttachment{FormerFileId: "deleted", MediaKind: contentv1.MissingAttachmentMediaKind(-7)}}}, DownloadAvailability: contentv1.ContentBlockDownloadAvailability(-1), DownloadAction: contentv1.ContentBlockDownloadAction(99)}
	expected.ProtoReflect().SetUnknown(unknown)
	expected.Selector.ProtoReflect().SetUnknown(unknown)
	expected.Attachment.ProtoReflect().SetUnknown(unknown)
	expected.Attachment.GetMissingAttachment().ProtoReflect().SetUnknown(unknown)
	got, err := proto.MarshalOptions{Deterministic: true}.Marshal(BlockMediaProto(item))
	require.NoError(t, err)
	want, err := proto.MarshalOptions{Deterministic: true}.Marshal(expected)
	require.NoError(t, err)
	require.Equal(t, want, got)
	for _, tc := range []struct {
		value *Attachment
		want  *contentv1.FileAttachment
	}{
		{nil, nil},
		{&Attachment{}, &contentv1.FileAttachment{}},
		{&Attachment{Kind: AttachmentActive}, &contentv1.FileAttachment{State: &contentv1.FileAttachment_ActiveFileId{ActiveFileId: ""}}},
		{&Attachment{Kind: AttachmentMissing}, &contentv1.FileAttachment{State: &contentv1.FileAttachment_MissingAttachment{}}},
	} {
		require.True(t, proto.Equal(tc.want, AttachmentProto(tc.value)))
	}
	clone := item.Clone()
	clone.Extensions[0] = 0
	clone.Selector.Extensions[0] = 0
	clone.Attachment.Extensions[0] = 0
	clone.Attachment.Missing.Extensions[0] = 0
	clone.Selector.BlockID = "other"
	clone.Attachment.Missing.FormerFileID = "other"
	require.Equal(t, byte(0xf8), item.Extensions[0])
	require.Equal(t, byte(0xf8), item.Selector.Extensions[0])
	require.Equal(t, byte(0xf8), item.Attachment.Extensions[0])
	require.Equal(t, byte(0xf8), item.Attachment.Missing.Extensions[0])
	require.Equal(t, "block", item.Selector.BlockID)
	require.Equal(t, "deleted", item.Attachment.Missing.FormerFileID)
	require.Nil(t, BlockMediaProto(nil))
	require.Nil(t, BlockMediaItemsProto(nil))
	require.NotNil(t, BlockMediaItemsProto([]*BlockMediaItem{}))
	require.Equal(t, []*contentv1.ContentBlockMediaItem{nil}, BlockMediaItemsProto([]*BlockMediaItem{nil}))
}
