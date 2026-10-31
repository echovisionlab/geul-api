package filemedia

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestUploadSessionStatusToProtoMapsKnownStatuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status model.UploadSessionStatus
		want   managev1.UploadSessionStatus
	}{
		{
			name:   "initiated",
			status: model.UploadSessionStatusInitiated,
			want:   managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_INITIATED,
		},
		{
			name:   "uploading",
			status: model.UploadSessionStatusUploading,
			want:   managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_UPLOADING,
		},
		{
			name:   "finalizing",
			status: model.UploadSessionStatusFinalizing,
			want:   managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_FINALIZING,
		},
		{
			name:   "failed",
			status: model.UploadSessionStatusFailed,
			want:   managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_FAILED,
		},
		{
			name:   "aborted",
			status: model.UploadSessionStatusAborted,
			want:   managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_ABORTED,
		},
		{
			name:   "unknown",
			status: model.UploadSessionStatus("paused"),
			want:   managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_UNSPECIFIED,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, uploadSessionStatusToProto(tt.status))
		})
	}
}

func TestNewStorageCompensationContextDetachesRequestCancellation(t *testing.T) {
	t.Parallel()

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	cleanupCtx, cancelCleanup := newStorageCompensationContext(requestCtx)
	defer cancelCleanup()

	require.NoError(t, cleanupCtx.Err())
	deadline, ok := cleanupCtx.Deadline()
	require.True(t, ok)
	require.WithinDuration(t, time.Now().Add(storageCompensationTimeout), deadline, time.Second)
}

func TestMultipartInitiateResponseFromSessionShapesFreshAndResumeResponses(t *testing.T) {
	t.Parallel()

	freshResp := multipartInitiateResponseFromSession(
		model.UploadSession{
			UploadID:      "upload-fresh",
			FileID:        "file-fresh",
			RequestedMime: "image/webp",
			TotalParts:    1,
			ChunkSize:     int32(chunkSize),
			Status:        model.UploadSessionStatusInitiated,
		},
		nil,
		false,
	)

	require.Equal(t, "upload-fresh", freshResp.GetUploadId())
	require.Equal(t, "file-fresh", freshResp.GetFileId())
	require.Equal(t, "webp", freshResp.GetExtension())
	require.EqualValues(t, 1, freshResp.GetTotalParts())
	require.EqualValues(t, chunkSize, freshResp.GetChunkSize())
	require.Equal(t, managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_INITIATED, freshResp.GetStatus())
	require.False(t, freshResp.GetResumed())
	require.Empty(t, freshResp.GetUploadedParts())

	attemptID := "attempt-1"
	uploadedParts := []*managev1.UploadPartInfo{
		{PartNumber: 1, Etag: "etag-1"},
		{PartNumber: 2, Etag: "etag-2"},
	}

	resp := multipartInitiateResponseFromSession(
		model.UploadSession{
			UploadID:      "upload-1",
			FileID:        "file-1",
			RequestedMime: "image/webp",
			TotalParts:    3,
			ChunkSize:     64,
			Status:        model.UploadSessionStatusUploading,
			AttemptID:     &attemptID,
		},
		uploadedParts,
		true,
	)

	require.Equal(t, "upload-1", resp.GetUploadId())
	require.Equal(t, "file-1", resp.GetFileId())
	require.Equal(t, "webp", resp.GetExtension())
	require.EqualValues(t, 3, resp.GetTotalParts())
	require.EqualValues(t, 64, resp.GetChunkSize())
	require.Equal(t, uploadedParts, resp.GetUploadedParts())
	require.Equal(t, managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_UPLOADING, resp.GetStatus())
	require.True(t, resp.GetResumed())
	require.Empty(t, resp.GetSlotId())
	require.Equal(t, attemptID, resp.GetIngestAttemptId())
}

func TestContainsFinalizingUploadSession(t *testing.T) {
	t.Parallel()

	require.False(t, containsFinalizingUploadSession(nil))
	require.False(t, containsFinalizingUploadSession([]model.UploadSession{{Status: model.UploadSessionStatusUploading}}))
	require.True(t, containsFinalizingUploadSession([]model.UploadSession{
		{Status: model.UploadSessionStatusUploading},
		{Status: model.UploadSessionStatusFinalizing},
	}))
}

func TestUploadSessionMatchesSelection(t *testing.T) {
	t.Parallel()

	storedLastModified := int64(1710000000000)
	requestLastModified := storedLastModified
	differentLastModified := storedLastModified + 1
	session := model.UploadSession{
		FileName:         "image.jpeg",
		FileSize:         1024,
		RequestedMime:    "image/jpeg",
		FileLastModified: &storedLastModified,
	}

	tests := []struct {
		name             string
		session          model.UploadSession
		fileName         string
		fileSize         int64
		mimeType         string
		fileLastModified *int64
		want             bool
	}{
		{
			name:     "matches without last modified request",
			session:  session,
			fileName: "image.jpeg",
			fileSize: 1024,
			mimeType: "image/jpeg",
			want:     true,
		},
		{
			name:             "matches with same last modified",
			session:          session,
			fileName:         "image.jpeg",
			fileSize:         1024,
			mimeType:         "image/jpeg",
			fileLastModified: &requestLastModified,
			want:             true,
		},
		{
			name:     "rejects file name mismatch",
			session:  session,
			fileName: "other.jpeg",
			fileSize: 1024,
			mimeType: "image/jpeg",
		},
		{
			name:     "rejects file size mismatch",
			session:  session,
			fileName: "image.jpeg",
			fileSize: 2048,
			mimeType: "image/jpeg",
		},
		{
			name:     "rejects mime mismatch",
			session:  session,
			fileName: "image.jpeg",
			fileSize: 1024,
			mimeType: "image/png",
		},
		{
			name:             "rejects missing stored last modified",
			session:          model.UploadSession{FileName: "image.jpeg", FileSize: 1024, RequestedMime: "image/jpeg"},
			fileName:         "image.jpeg",
			fileSize:         1024,
			mimeType:         "image/jpeg",
			fileLastModified: &requestLastModified,
		},
		{
			name:             "rejects different last modified",
			session:          session,
			fileName:         "image.jpeg",
			fileSize:         1024,
			mimeType:         "image/jpeg",
			fileLastModified: &differentLastModified,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, uploadSessionMatchesSelection(
				tt.session,
				tt.fileName,
				tt.fileSize,
				tt.mimeType,
				tt.fileLastModified,
			))
		})
	}
}

func TestMultipartSessionChunkSizeUsesStoredOrDefaultChunkSize(t *testing.T) {
	t.Parallel()

	require.Equal(t, int64(9), multipartSessionChunkSize(model.UploadSession{ChunkSize: 9}))
	require.Equal(t, int64(chunkSize), multipartSessionChunkSize(model.UploadSession{}))
}

func TestUploadPartConversionsPreservePartOrder(t *testing.T) {
	t.Parallel()

	parts := []model.UploadPart{
		{UploadID: "upload-1", PartNumber: 2, ETag: "etag-2"},
		{UploadID: "upload-1", PartNumber: 1, ETag: "etag-1"},
	}

	completedParts := toCompletedParts(parts)
	require.Len(t, completedParts, 2)
	require.EqualValues(t, 2, aws.ToInt32(completedParts[0].PartNumber))
	require.Equal(t, "etag-2", aws.ToString(completedParts[0].ETag))
	require.EqualValues(t, 1, aws.ToInt32(completedParts[1].PartNumber))
	require.Equal(t, "etag-1", aws.ToString(completedParts[1].ETag))

	uploadInfos := uploadPartInfos(parts)
	require.Len(t, uploadInfos, 2)
	require.EqualValues(t, 2, uploadInfos[0].GetPartNumber())
	require.Equal(t, "etag-2", uploadInfos[0].GetEtag())
	require.EqualValues(t, 1, uploadInfos[1].GetPartNumber())
	require.Equal(t, "etag-1", uploadInfos[1].GetEtag())

	require.Equal(t, []int32{2, 1}, uploadPartModelNumbers(parts))
}
