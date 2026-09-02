package filemedia

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type fakeMCPFileRuntime struct {
	initiateRequest *managev1.InitiateMultipartUploadRequest
	initiateResult  *managev1.InitiateMultipartUploadResponse
	initiateError   error

	findRequest *managev1.FindMultipartUploadCandidateRequest
	findResult  *managev1.FindMultipartUploadCandidateResponse
	findError   error

	completeRequest *managev1.CompleteMultipartUploadRequest
	completeResult  *managev1.CompleteMultipartUploadResponse
	completeError   error

	downloadRequest *managev1.DownloadFromUrlRequest
	downloadResult  *managev1.DownloadFromUrlResponse
	downloadError   error

	deliveryRequest *managev1.GetMediaDeliveryRequest
	deliveryResult  *managev1.GetMediaDeliveryResponse
	deliveryError   error
}

func (runtime *fakeMCPFileRuntime) InitiateMultipartUpload(
	_ context.Context,
	request *connect.Request[managev1.InitiateMultipartUploadRequest],
) (*connect.Response[managev1.InitiateMultipartUploadResponse], error) {
	runtime.initiateRequest = request.Msg
	if runtime.initiateError != nil {
		return nil, runtime.initiateError
	}
	return connect.NewResponse(runtime.initiateResult), nil
}

func (runtime *fakeMCPFileRuntime) FindMultipartUploadCandidate(
	_ context.Context,
	request *connect.Request[managev1.FindMultipartUploadCandidateRequest],
) (*connect.Response[managev1.FindMultipartUploadCandidateResponse], error) {
	runtime.findRequest = request.Msg
	if runtime.findError != nil {
		return nil, runtime.findError
	}
	return connect.NewResponse(runtime.findResult), nil
}

func (runtime *fakeMCPFileRuntime) CompleteMultipartUpload(
	_ context.Context,
	request *connect.Request[managev1.CompleteMultipartUploadRequest],
) (*connect.Response[managev1.CompleteMultipartUploadResponse], error) {
	runtime.completeRequest = request.Msg
	if runtime.completeError != nil {
		return nil, runtime.completeError
	}
	return connect.NewResponse(runtime.completeResult), nil
}

func (runtime *fakeMCPFileRuntime) DownloadFromUrl(
	_ context.Context,
	request *connect.Request[managev1.DownloadFromUrlRequest],
) (*connect.Response[managev1.DownloadFromUrlResponse], error) {
	runtime.downloadRequest = request.Msg
	if runtime.downloadError != nil {
		return nil, runtime.downloadError
	}
	return connect.NewResponse(runtime.downloadResult), nil
}

func (runtime *fakeMCPFileRuntime) GetMediaDelivery(
	_ context.Context,
	request *connect.Request[managev1.GetMediaDeliveryRequest],
) (*connect.Response[managev1.GetMediaDeliveryResponse], error) {
	runtime.deliveryRequest = request.Msg
	if runtime.deliveryError != nil {
		return nil, runtime.deliveryError
	}
	return connect.NewResponse(runtime.deliveryResult), nil
}

func TestNewMCPFileFacadeRejectsMissingRuntime(t *testing.T) {
	_, err := NewMCPFileFacade(nil)
	if !errors.Is(err, ErrInvalidMCPFileDependency) {
		t.Fatalf("NewMCPFileFacade() error = %v", err)
	}
}

func TestMCPFileBeginMultipartUsesIndependentFileIngest(t *testing.T) {
	t.Parallel()

	fileID := uuid.NewString()
	kinds := map[MCPFileKind]managev1.UploadType{
		MCPFileKindGeneral:    managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE,
		MCPFileKindImage:      managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE,
		MCPFileKindVideo:      managev1.UploadType_UPLOAD_TYPE_EDITOR_VIDEO,
		MCPFileKindAudio:      managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO,
		MCPFileKindAttachment: managev1.UploadType_UPLOAD_TYPE_EDITOR_ATTACHMENT,
		MCPFileKindMesh:       managev1.UploadType_UPLOAD_TYPE_EDITOR_MESH,
	}
	for kind, wantUploadType := range kinds {
		kind, wantUploadType := kind, wantUploadType
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			runtime := &fakeMCPFileRuntime{initiateResult: &managev1.InitiateMultipartUploadResponse{
				FileId: fileID, UploadId: "upload-1", TotalParts: 2, ChunkSize: 1024,
				Status: managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_INITIATED,
			}}
			facade, _ := NewMCPFileFacade(runtime)
			modified := int64(123)
			result, err := facade.Begin(context.Background(), MCPFileBeginInput{
				Kind: kind, Transport: MCPFileTransportPresignedMultipart,
				FileName: "asset.bin", MIMEType: "application/octet-stream", FileSize: 2048,
				FileLastModified: &modified,
			})
			if err != nil {
				t.Fatalf("Begin() error = %v", err)
			}
			if runtime.initiateRequest.GetUploadType() != wantUploadType ||
				runtime.initiateRequest.GetEntityId() != "" || runtime.initiateRequest.GetSlotId() != "" {
				t.Fatalf("Initiate request = %#v", runtime.initiateRequest)
			}
			if result.Session == nil || result.Session.Handle.FileID != fileID || result.File != nil {
				t.Fatalf("Begin() result = %#v", result)
			}
		})
	}
}

func TestMCPFileBeginRemoteHTTPSDelegatesToVerifiedImport(t *testing.T) {
	t.Parallel()

	fileID := uuid.NewString()
	runtime := &fakeMCPFileRuntime{downloadResult: &managev1.DownloadFromUrlResponse{
		FileId: fileID, Delivery: minimalDelivery(fileID),
	}}
	facade, _ := NewMCPFileFacade(runtime)
	result, err := facade.Begin(context.Background(), MCPFileBeginInput{
		Kind: MCPFileKindImage, Transport: MCPFileTransportRemoteHTTPS,
		RemoteURL: "https://example.com/image.png?size=large",
	})
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	if runtime.downloadRequest.GetUploadType() != managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE ||
		runtime.downloadRequest.GetUrl() != "https://example.com/image.png?size=large" {
		t.Fatalf("Download request = %#v", runtime.downloadRequest)
	}
	if result.State != MCPFileTransferReady || result.File == nil || result.File.ID != fileID || result.Session != nil {
		t.Fatalf("Begin() result = %#v", result)
	}
}

func TestMCPFileBeginRejectsInlinePayloadFormsBeforeRuntime(t *testing.T) {
	t.Parallel()

	invalidURLs := []string{
		"data:image/png;base64,AA==",
		"http://example.com/file.png",
		"https://user:password@example.com/file.png",
		"https://example.com/file.png#fragment",
		"AA==",
	}
	for _, invalidURL := range invalidURLs {
		invalidURL := invalidURL
		t.Run(invalidURL, func(t *testing.T) {
			t.Parallel()
			runtime := &fakeMCPFileRuntime{}
			facade, _ := NewMCPFileFacade(runtime)
			_, err := facade.Begin(context.Background(), MCPFileBeginInput{
				Kind: MCPFileKindGeneral, Transport: MCPFileTransportRemoteHTTPS, RemoteURL: invalidURL,
			})
			if !errors.Is(err, ErrInvalidMCPFileInput) || runtime.downloadRequest != nil {
				t.Fatalf("Begin() error = %v, runtime called = %v", err, runtime.downloadRequest != nil)
			}
		})
	}
}

func TestMCPFileInputTypesCannotRepresentFilePayload(t *testing.T) {
	t.Parallel()

	for _, inputType := range []reflect.Type{
		reflect.TypeOf(MCPFileBeginInput{}),
		reflect.TypeOf(MCPFileSessionHandle{}),
	} {
		for index := 0; index < inputType.NumField(); index++ {
			field := inputType.Field(index)
			name := strings.ToLower(field.Name)
			for _, forbidden := range []string{"base64", "bytes", "content", "payload", "datauri"} {
				if strings.Contains(name, forbidden) {
					t.Fatalf("%s unexpectedly exposes %s", inputType, field.Name)
				}
			}
			if field.Type.Kind() == reflect.Slice && field.Type.Elem().Kind() == reflect.Uint8 {
				t.Fatalf("%s unexpectedly exposes byte slice %s", inputType, field.Name)
			}
		}
	}
}

func TestMCPFileStatusReturnsOnlyCompactPartProgress(t *testing.T) {
	t.Parallel()

	fileID := uuid.NewString()
	lastActivity := timestamppb.New(time.Date(2026, 8, 23, 1, 2, 3, 0, time.FixedZone("KST", 9*60*60)))
	runtime := &fakeMCPFileRuntime{findResult: &managev1.FindMultipartUploadCandidateResponse{
		FileId: pointer(fileID), UploadId: pointer("upload-1"), TotalParts: 3, ChunkSize: 1024,
		Status:        managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_UPLOADING,
		UploadedParts: []*managev1.UploadPartInfo{{PartNumber: 1, Etag: "must-not-leak"}, {PartNumber: 3, Etag: "must-not-leak"}},
		FileName:      pointer("audio.wav"), MimeType: pointer("audio/wav"), FileSize: 3072,
		LastActivityAt: lastActivity,
	}}
	facade, _ := NewMCPFileFacade(runtime)
	handle := MCPFileSessionHandle{
		Transport: MCPFileTransportBrowserUploadPage, Kind: MCPFileKindAudio,
		FileID: fileID, UploadID: "upload-1",
	}
	result, err := facade.Status(context.Background(), handle)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if runtime.findRequest.GetFileId() != fileID || runtime.findRequest.GetUploadId() != "upload-1" ||
		runtime.findRequest.GetUploadType() != managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO {
		t.Fatalf("Find request = %#v", runtime.findRequest)
	}
	if result.Session == nil || !reflect.DeepEqual(result.Session.UploadedPartNumbers, []int32{1, 3}) ||
		result.Session.LastActivityAt == nil || result.Session.LastActivityAt.Location() != time.UTC {
		t.Fatalf("Status() result = %#v", result)
	}
}

func TestMCPFileStatusFallsBackToAuthorizedDeliveryAfterSessionRemoval(t *testing.T) {
	t.Parallel()

	fileID := uuid.NewString()
	runtime := &fakeMCPFileRuntime{
		findResult:     &managev1.FindMultipartUploadCandidateResponse{},
		deliveryResult: &managev1.GetMediaDeliveryResponse{Delivery: minimalDelivery(fileID)},
	}
	facade, _ := NewMCPFileFacade(runtime)
	result, err := facade.Status(context.Background(), MCPFileSessionHandle{
		Transport: MCPFileTransportPresignedMultipart, Kind: MCPFileKindGeneral,
		FileID: fileID, UploadID: "upload-1",
	})
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if result.State != MCPFileTransferReady || result.File == nil || runtime.deliveryRequest.GetFileId() != fileID {
		t.Fatalf("Status() result = %#v", result)
	}
}

func TestMCPFileCompleteAndReadReturnBoundedVerifiedHandle(t *testing.T) {
	t.Parallel()

	fileID := uuid.NewString()
	delivery := completeDelivery(fileID)
	runtime := &fakeMCPFileRuntime{
		completeResult: &managev1.CompleteMultipartUploadResponse{FileId: fileID, Delivery: delivery},
		deliveryResult: &managev1.GetMediaDeliveryResponse{Delivery: delivery},
	}
	facade, _ := NewMCPFileFacade(runtime)
	handle := MCPFileSessionHandle{
		Transport: MCPFileTransportPresignedMultipart, Kind: MCPFileKindVideo,
		FileID: fileID, UploadID: "upload-1",
	}
	result, err := facade.Complete(context.Background(), handle)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if runtime.completeRequest.GetFileId() != fileID || runtime.completeRequest.GetUploadId() != "upload-1" {
		t.Fatalf("Complete request = %#v", runtime.completeRequest)
	}
	if result.File == nil || len(result.File.References) != 7 || result.File.DerivativeStatus != "ready" {
		t.Fatalf("Complete() result = %#v", result)
	}
	read, err := facade.Read(context.Background(), fileID)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if read.ID != fileID || len(read.References) != 7 || runtime.deliveryRequest.GetFileId() != fileID {
		t.Fatalf("Read() result = %#v", read)
	}
}

func TestMCPFileFacadeFailsClosedOnRuntimeMismatchAndPreservesAuthorityErrors(t *testing.T) {
	t.Parallel()

	fileID := uuid.NewString()
	otherID := uuid.NewString()
	runtime := &fakeMCPFileRuntime{deliveryResult: &managev1.GetMediaDeliveryResponse{Delivery: minimalDelivery(otherID)}}
	facade, _ := NewMCPFileFacade(runtime)
	_, err := facade.Read(context.Background(), fileID)
	if !errors.Is(err, ErrInvalidMCPFileRuntime) {
		t.Fatalf("Read() mismatch error = %v", err)
	}

	authorityError := connect.NewError(connect.CodePermissionDenied, errors.New("denied"))
	runtime.deliveryError = authorityError
	_, err = facade.Read(context.Background(), fileID)
	if !errors.Is(err, authorityError) {
		t.Fatalf("Read() authority error = %v", err)
	}
}

func minimalDelivery(fileID string) *commonv1.MediaDelivery {
	return &commonv1.MediaDelivery{
		FileId: fileID, Extension: "bin", MimeType: "application/octet-stream", FileSize: 1,
	}
}

func completeDelivery(fileID string) *commonv1.MediaDelivery {
	expiresAt := timestamppb.New(time.Now().Add(time.Minute))
	fileName := "video.mp4"
	downloadName := "video-download.mp4"
	percentage := int32(100)
	asset := func(id, path string) *commonv1.AssetRef {
		return &commonv1.AssetRef{
			AssetId: id, Url: "https://cdn.example.com/" + path, Extension: "mp4", MimeType: "video/mp4",
			FileSize: 42, Sha256: []byte("must-not-leak"), DownloadFilename: &downloadName,
		}
	}
	return &commonv1.MediaDelivery{
		FileId: fileID, FileName: &fileName, Extension: "mp4", MimeType: "video/mp4", FileSize: 42,
		Inline: &commonv1.ExpiringMediaRef{
			FileId: fileID, Url: "https://files.example.com/inline", ExpiresAt: expiresAt,
			Extension: "mp4", MimeType: "video/mp4", FileName: &fileName,
		},
		Download: &commonv1.ExpiringMediaRef{
			FileId: fileID, Url: "https://files.example.com/download", ExpiresAt: expiresAt,
			Extension: "mp4", MimeType: "video/mp4", FileName: &fileName,
		},
		Asset:                asset("asset", "asset"),
		Playback:             &commonv1.HlsMediaRef{FileId: fileID, GenerationId: "generation", Url: "https://cdn.example.com/playback.m3u8"},
		Thumbnail:            asset("thumbnail", "thumbnail"),
		Spectrogram:          asset("spectrogram", "spectrogram"),
		Waveform:             asset("waveform", "waveform"),
		ProcessingStatus:     commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_READY,
		ProcessingPercentage: &percentage,
	}
}
