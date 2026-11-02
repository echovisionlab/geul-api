package filemedia

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestUploadViewsPreservePartsOptionalIdentityAndActivity(t *testing.T) {
	t.Parallel()
	at := time.Unix(2000000000, 123).UTC()
	parts := []*UploadedPart{{PartNumber: 2, ETag: "etag"}, nil}
	wireParts := []*managev1.UploadPartInfo{{PartNumber: 2, Etag: "etag"}, nil}
	requireMediaWire(t, &managev1.InitiateMultipartUploadResponse{UploadId: "upload", FileId: "file", Extension: "mp4", TotalParts: 3, ChunkSize: 64, UploadedParts: wireParts, Status: managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_FINALIZING, Resumed: true, SlotId: new(""), IngestAttemptId: new("attempt"), ExpectedCurrentFileId: new("")}, multipartUploadMessage(&MultipartUpload{UploadID: "upload", FileID: "file", Extension: "mp4", TotalParts: 3, ChunkSize: 64, UploadedParts: parts, Status: domain.UploadSessionStatusFinalizing, Resumed: true, SlotID: new(""), IngestAttemptID: new("attempt"), ExpectedCurrentFileID: new("")}))
	requireMediaWire(t, &managev1.FindMultipartUploadCandidateResponse{ClientMediaBundleId: new("bundle"), UploadId: new("upload"), FileId: new("file"), Extension: new("mp4"), TotalParts: 3, ChunkSize: 64, Status: managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_UPLOADING, FileName: new(""), MimeType: new("video/mp4"), FileSize: 129, FileLastModified: new(int64(0)), LastActivityAt: timestamppb.New(at), SlotId: new(""), IngestAttemptId: new("attempt"), ExpectedCurrentFileId: new(""), UploadedParts: wireParts}, multipartCandidateMessage(&MultipartCandidate{ClientMediaBundleID: new("bundle"), UploadID: new("upload"), FileID: new("file"), Extension: new("mp4"), TotalParts: 3, ChunkSize: 64, Status: domain.UploadSessionStatusUploading, FileName: new(""), MimeType: new("video/mp4"), FileSize: 129, FileLastModified: new(int64(0)), LastActivityAt: &at, SlotID: new(""), IngestAttemptID: new("attempt"), ExpectedCurrentFileID: new(""), UploadedParts: parts}))
	empty := multipartCandidateMessage(&MultipartCandidate{})
	require.Nil(t, empty.FileId)
	require.Nil(t, empty.UploadId)
	require.Nil(t, empty.LastActivityAt)
	require.Nil(t, empty.UploadedParts)
	require.Equal(t, managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_UNSPECIFIED, multipartCandidateMessage(&MultipartCandidate{Status: "corrupt"}).Status)
}
