package mediaauth

import (
	"testing"
	"time"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestReferenceCloneKeepsDeliveryMetadataAndOwnsItsFilename(t *testing.T) {
	var absent *Reference
	require.Nil(t, absent.Clone())

	filename := "source.wav"
	original := &Reference{
		FileID: testFileID, URL: "https://cdn.example.com/media/signed/source.wav",
		Extension: "wav", MimeType: "audio/wav", FileName: &filename,
		Purpose: PurposeDownload, ExpiresAt: time.Date(2026, 11, 2, 1, 2, 3, 123456789, time.UTC),
	}
	cloned := original.Clone()
	require.Equal(t, original, cloned)
	require.NotSame(t, original, cloned)
	require.NotSame(t, original.FileName, cloned.FileName)
	*cloned.FileName = "renamed.wav"
	cloned.URL = "https://cdn.example.com/changed"
	require.Equal(t, "source.wav", *original.FileName)
	require.Equal(t, "https://cdn.example.com/media/signed/source.wav", original.URL)

	original.FileName = nil
	withoutFilename := original.Clone()
	require.Equal(t, original, withoutFilename)
	require.NotSame(t, original, withoutFilename)
	require.Nil(t, withoutFilename.FileName)
}

func TestReferenceProtoPreservesTheSignedDownloadContract(t *testing.T) {
	require.Nil(t, ReferenceProto(nil))

	filename := "source"
	ref, err := ExpiringReference("cdn.example.com", "secret", testFileID, "wav", "audio/wav", &filename, PurposeDownload, time.Minute)
	require.NoError(t, err)
	claims := signedClaims(t, ref.URL)
	got := ReferenceProto(ref)
	want := &commonv1.ExpiringMediaRef{
		FileId: testFileID, Url: ref.URL,
		Purpose:   commonv1.MediaDeliveryPurpose_MEDIA_DELIVERY_PURPOSE_DOWNLOAD,
		ExpiresAt: timestamppb.New(time.Unix(claims.ExpiryUnix, 0)),
		Extension: "wav", MimeType: "audio/wav", FileName: proto.String("source.wav"),
	}
	require.True(t, proto.Equal(want, got), "wire reference = %v, want %v", got, want)
	require.Equal(t, claims.Filename, got.GetFileName())

	ref, err = ExpiringReference("cdn.example.com", "secret", testFileID, "wav", "audio/wav", nil, PurposeInline, time.Minute)
	require.NoError(t, err)
	claims = signedClaims(t, ref.URL)
	got = ReferenceProto(ref)
	require.Nil(t, got.FileName)
	require.Empty(t, claims.Filename)
	require.Equal(t, PurposeInline, claims.Purpose)
	require.Equal(t, commonv1.MediaDeliveryPurpose_MEDIA_DELIVERY_PURPOSE_INLINE, got.Purpose)
	require.True(t, got.ExpiresAt.AsTime().Equal(time.Unix(claims.ExpiryUnix, 0)))
}
