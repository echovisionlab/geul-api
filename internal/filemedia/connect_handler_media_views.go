package filemedia

import (
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	filepublic "github.com/echovisionlab/geul-api/internal/filemedia/public"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func DeliveryMessage(value *Delivery) *commonv1.MediaDelivery { return filepublic.DeliveryProto(value) }
func MediaDeliveryResultMessage(value *MediaDeliveryResult) *managev1.GetMediaDeliveryResponse {
	if value == nil {
		return nil
	}
	return &managev1.GetMediaDeliveryResponse{Delivery: DeliveryMessage(value.Delivery), IngestSlotId: value.IngestSlotID, IngestAttemptId: value.IngestAttemptID, ClientMediaBundleId: value.ClientMediaBundleID}
}
func mediaDeliveryResultsMessage(values map[string]*MediaDeliveryResult) map[string]*managev1.GetMediaDeliveryResponse {
	if values == nil {
		return nil
	}
	result := make(map[string]*managev1.GetMediaDeliveryResponse, len(values))
	for id, value := range values {
		result[id] = MediaDeliveryResultMessage(value)
	}
	return result
}
func CompletedUploadMessage(value *CompletedUpload) *managev1.CompleteMultipartUploadResponse {
	if value == nil {
		return nil
	}
	return &managev1.CompleteMultipartUploadResponse{Delivery: DeliveryMessage(value.Delivery), FileId: value.FileID}
}
func ImportedFileMessage(value *ImportedFile) *managev1.DownloadFromUrlResponse {
	if value == nil {
		return nil
	}
	return &managev1.DownloadFromUrlResponse{Delivery: DeliveryMessage(value.Delivery), FileId: value.FileID, SlotId: value.SlotID, IngestAttemptId: value.IngestAttemptID, ExpectedCurrentFileId: value.ExpectedCurrentFileID}
}
func libraryFileMessage(value *LibraryFile) *managev1.FileManagerFile {
	if value == nil {
		return nil
	}
	result := &managev1.FileManagerFile{Id: value.ID, FileName: value.FileName, Extension: value.Extension, MimeType: value.MimeType, FileSize: value.FileSize, DurationSeconds: value.DurationSeconds, FolderId: value.FolderID, UploadedByMember: memberSummaryProto(value.UploadedByMember), Delivery: DeliveryMessage(value.Delivery), UsageCount: value.UsageCount}
	if value.CreatedAt != nil {
		result.CreatedAt = timestamppb.New(*value.CreatedAt)
	}
	if value.UpdatedAt != nil {
		result.UpdatedAt = timestamppb.New(*value.UpdatedAt)
	}
	return result
}
func libraryFilesMessage(values []*LibraryFile) []*managev1.FileManagerFile {
	if values == nil {
		return nil
	}
	result := make([]*managev1.FileManagerFile, len(values))
	for i, value := range values {
		result[i] = libraryFileMessage(value)
	}
	return result
}
func fileDetailsMessage(value *FileDetails) *managev1.GetFileResponse {
	if value == nil {
		return nil
	}
	result := &managev1.GetFileResponse{File: libraryFileMessage(value.File), DomainUsageSummary: fileUsageViewsProto(value.UsageSummary)}
	if value.GeneratedOutputs != nil {
		result.GeneratedOutputs = make([]*managev1.FileGeneratedOutput, len(value.GeneratedOutputs))
		for i, output := range value.GeneratedOutputs {
			if output != nil {
				result.GeneratedOutputs[i] = &managev1.FileGeneratedOutput{Id: output.ID, Type: managev1.FileDerivativeType(output.Type), Status: commonv1.MediaProcessingStatus(output.Status), Delivery: DeliveryMessage(output.Delivery)}
			}
		}
	}
	return result
}
func libraryItemsMessage(values []*LibraryItem) []*managev1.FileManagerItem {
	if values == nil {
		return nil
	}
	result := make([]*managev1.FileManagerItem, len(values))
	for i, value := range values {
		if value == nil {
			continue
		}
		item := &managev1.FileManagerItem{Type: managev1.FileManagerItemType(value.Type)}
		switch value.Type {
		case domain.LibraryItemFolder:
			item.Item = &managev1.FileManagerItem_Folder{Folder: folderViewProto(value.Folder)}
		case domain.LibraryItemFile:
			item.Item = &managev1.FileManagerItem_File{File: libraryFileMessage(value.File)}
		}
		if value.FolderPath != nil {
			item.FolderPath = make([]*managev1.FileManagerPathSegment, len(value.FolderPath))
			for j, segment := range value.FolderPath {
				if segment != nil {
					item.FolderPath[j] = &managev1.FileManagerPathSegment{Id: segment.ID, Name: segment.Name}
				}
			}
		}
		result[i] = item
	}
	return result
}
