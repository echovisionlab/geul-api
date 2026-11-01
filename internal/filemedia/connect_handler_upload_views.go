package filemedia

import (
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func uploadedPartsMessage(values []*UploadedPart) []*managev1.UploadPartInfo {
	if values == nil {
		return nil
	}
	result := make([]*managev1.UploadPartInfo, len(values))
	for i, value := range values {
		if value != nil {
			result[i] = &managev1.UploadPartInfo{PartNumber: value.PartNumber, Etag: value.ETag}
		}
	}
	return result
}

func multipartUploadMessage(value *MultipartUpload) *managev1.InitiateMultipartUploadResponse {
	if value == nil {
		return nil
	}
	return &managev1.InitiateMultipartUploadResponse{UploadId: value.UploadID, FileId: value.FileID, Extension: value.Extension, TotalParts: value.TotalParts, ChunkSize: value.ChunkSize, UploadedParts: uploadedPartsMessage(value.UploadedParts), Status: uploadSessionStatusToProto(value.Status), Resumed: value.Resumed, SlotId: value.SlotID, IngestAttemptId: value.IngestAttemptID, ExpectedCurrentFileId: value.ExpectedCurrentFileID}
}

func multipartCandidateMessage(value *MultipartCandidate) *managev1.FindMultipartUploadCandidateResponse {
	if value == nil {
		return nil
	}
	result := &managev1.FindMultipartUploadCandidateResponse{ClientMediaBundleId: value.ClientMediaBundleID, UploadId: value.UploadID, FileId: value.FileID, Extension: value.Extension, TotalParts: value.TotalParts, ChunkSize: value.ChunkSize, Status: uploadSessionStatusToProto(value.Status), FileName: value.FileName, FileSize: value.FileSize, MimeType: value.MimeType, FileLastModified: value.FileLastModified, SlotId: value.SlotID, IngestAttemptId: value.IngestAttemptID, ExpectedCurrentFileId: value.ExpectedCurrentFileID, UploadedParts: uploadedPartsMessage(value.UploadedParts)}
	if value.LastActivityAt != nil {
		result.LastActivityAt = timestamppb.New(*value.LastActivityAt)
	}
	return result
}

func uploadSessionStatusToProto(status domain.UploadSessionStatus) managev1.UploadSessionStatus {
	switch status {
	case domain.UploadSessionStatusInitiated:
		return managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_INITIATED
	case domain.UploadSessionStatusUploading:
		return managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_UPLOADING
	case domain.UploadSessionStatusFinalizing:
		return managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_FINALIZING
	case domain.UploadSessionStatusFailed:
		return managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_FAILED
	case domain.UploadSessionStatusAborted:
		return managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_ABORTED
	default:
		return managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_UNSPECIFIED
	}
}
