package filemedia

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func requireMediaWire(t *testing.T, want, got proto.Message) {
	t.Helper()
	expected, err := (proto.MarshalOptions{Deterministic: true}).Marshal(want)
	require.NoError(t, err)
	actual, err := (proto.MarshalOptions{Deterministic: true}).Marshal(got)
	require.NoError(t, err)
	require.Equal(t, expected, actual)
}

func TestMediaResultsPreserveOptionalIDsAndDetachedFenceCopies(t *testing.T) {
	t.Parallel()
	result := &MediaDeliveryResult{Delivery: &Delivery{FileID: "file", ProcessingStatus: ProcessingStatus(99), ProcessingPercentage: new(int32(0))}, IngestSlotID: new(""), IngestAttemptID: new("attempt"), ClientMediaBundleID: new("bundle")}
	wireDelivery := &commonv1.MediaDelivery{FileId: "file", ProcessingStatus: commonv1.MediaProcessingStatus(99), ProcessingPercentage: new(int32(0))}
	requireMediaWire(t, &managev1.GetMediaDeliveryResponse{Delivery: wireDelivery, IngestSlotId: new(""), IngestAttemptId: new("attempt"), ClientMediaBundleId: new("bundle")}, MediaDeliveryResultMessage(result))
	requireMediaWire(t, &managev1.CompleteMultipartUploadResponse{Delivery: wireDelivery, FileId: "file"}, CompletedUploadMessage(&CompletedUpload{FileID: "file", Delivery: result.Delivery}))
	requireMediaWire(t, &managev1.DownloadFromUrlResponse{Delivery: wireDelivery, FileId: "file", SlotId: new(""), IngestAttemptId: new("attempt"), ExpectedCurrentFileId: new("")}, ImportedFileMessage(&ImportedFile{FileID: "file", Delivery: result.Delivery, SlotID: new(""), IngestAttemptID: new("attempt"), ExpectedCurrentFileID: new("")}))
	clone := result.Clone()
	*clone.IngestSlotID, *clone.IngestAttemptID, *clone.ClientMediaBundleID = "changed", "changed", "changed"
	*clone.Delivery.ProcessingPercentage = 99
	require.Empty(t, *result.IngestSlotID)
	require.Equal(t, "attempt", *result.IngestAttemptID)
	require.Equal(t, "bundle", *result.ClientMediaBundleID)
	require.Zero(t, *result.Delivery.ProcessingPercentage)
	require.Nil(t, MediaDeliveryResultMessage(nil))
	require.Nil(t, MediaDeliveryResultMessage(&MediaDeliveryResult{}).IngestSlotId)
}

func TestLibraryViewsPreserveMetadataGeneratedOutputsAndItemArms(t *testing.T) {
	t.Parallel()
	at := time.Unix(2000000000, 123).In(time.FixedZone("KST", 9*60*60))
	file := &LibraryFile{ID: "file", FileName: "파일", Extension: "mp4", MimeType: "video/mp4", FileSize: 64, DurationSeconds: new(int32(0)), FolderID: new(""), UploadedByMember: &MemberSummary{ID: "member", Deleted: true}, CreatedAt: &at, UpdatedAt: new(time.Time{}), UsageCount: 3}
	wireFile := &managev1.FileManagerFile{Id: "file", FileName: "파일", Extension: "mp4", MimeType: "video/mp4", FileSize: 64, DurationSeconds: new(int32(0)), FolderId: new(""), UploadedByMember: &commonv1.MemberSummary{Id: "member", Deleted: true}, CreatedAt: timestamppb.New(at), UpdatedAt: timestamppb.New(time.Time{}), UsageCount: 3}
	requireMediaWire(t, &managev1.GetFileResponse{File: wireFile, GeneratedOutputs: []*managev1.FileGeneratedOutput{{Id: "output", Type: managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_OPTIMIZED_MESH, Status: commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_FAILED}}}, fileDetailsMessage(&FileDetails{File: file, GeneratedOutputs: []*GeneratedOutput{{ID: "output", Type: DerivativeOptimizedMesh, Status: ProcessingFailed}}}))
	values := libraryItemsMessage([]*LibraryItem{{Type: LibraryItemFile, File: file, FolderPath: []*FolderPathSegment{{ID: "folder", Name: "Folder"}}}, {Type: LibraryItemFolder, Folder: &FolderView{ID: "folder", Name: "Folder"}}, nil})
	requireMediaWire(t, &managev1.FileManagerItem{Type: managev1.FileManagerItemType_FILE_MANAGER_ITEM_TYPE_FILE, Item: &managev1.FileManagerItem_File{File: wireFile}, FolderPath: []*managev1.FileManagerPathSegment{{Id: "folder", Name: "Folder"}}}, values[0])
	requireMediaWire(t, &managev1.FileManagerItem{Type: managev1.FileManagerItemType_FILE_MANAGER_ITEM_TYPE_FOLDER, Item: &managev1.FileManagerItem_Folder{Folder: &managev1.FileFolder{Id: "folder", Name: "Folder"}}}, values[1])
	require.Nil(t, values[2])
	require.Nil(t, libraryFileMessage(&LibraryFile{}).CreatedAt)
}

func TestDerivativeNamesAndProcessingOrdinalsMatchPersistedContracts(t *testing.T) {
	t.Parallel()
	for ordinal, name := range managev1.FileDerivativeType_name {
		kind, ok := domain.ParseDerivativeKind(name)
		require.True(t, ok, name)
		require.EqualValues(t, ordinal, kind)
		require.Equal(t, name, kind.String())
	}
	_, ok := domain.ParseDerivativeKind("unknown")
	require.False(t, ok)
	require.Equal(t, "99", DerivativeKind(99).String())
	for ordinal, name := range commonv1.MediaProcessingStatus_name {
		require.Equal(t, name, ProcessingStatus(ordinal).String())
	}
}
