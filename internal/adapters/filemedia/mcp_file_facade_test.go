package filemedia

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	filemediadomain "github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type fakeMCPFileRuntime struct {
	importInput     *filemediadomain.RemoteFileImportInput
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

func (runtime *fakeMCPFileRuntime) ImportRemoteFile(_ context.Context, input filemediadomain.RemoteFileImportInput) (*managev1.DownloadFromUrlResponse, error) {
	runtime.importInput = &input
	return runtime.downloadResult, runtime.downloadError
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

func TestMCPFileUploadUsesStandaloneNativeImportForAllKinds(t *testing.T) {
	fileID, correlationID := uuid.NewString(), uuid.NewString()
	for kind, uploadType := range map[MCPFileKind]managev1.UploadType{
		MCPFileKindGeneral:    managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE,
		MCPFileKindImage:      managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE,
		MCPFileKindVideo:      managev1.UploadType_UPLOAD_TYPE_EDITOR_VIDEO,
		MCPFileKindAudio:      managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO,
		MCPFileKindAttachment: managev1.UploadType_UPLOAD_TYPE_EDITOR_ATTACHMENT,
		MCPFileKindMesh:       managev1.UploadType_UPLOAD_TYPE_EDITOR_MESH,
	} {
		t.Run(string(kind), func(t *testing.T) {
			runtime := &fakeMCPFileRuntime{downloadResult: &managev1.DownloadFromUrlResponse{FileId: fileID, Delivery: &commonv1.MediaDelivery{FileId: fileID, Extension: "txt", MimeType: "text/plain", FileSize: 12}}}
			facade, err := NewMCPFileFacade(runtime)
			if err != nil {
				t.Fatal(err)
			}
			file, err := facade.Upload(t.Context(), MCPFileUploadInput{DownloadURL: "https://example.com/download?sig=private", FileID: "opaque-not-a-uuid", FileName: "original.txt", MIMEType: "image/png"}, kind, correlationID)
			if err != nil {
				t.Fatal(err)
			}
			want := filemediadomain.RemoteFileImportInput{UploadType: uploadType, SourceURL: "https://example.com/download?sig=private", FileName: "original.txt", CorrelationID: correlationID}
			if runtime.importInput == nil || *runtime.importInput != want || file.ID != fileID || file.MIMEType != "text/plain" {
				t.Fatalf("input/file = %+v / %+v", runtime.importInput, file)
			}
		})
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
		RemoteURL:     "https://example.com/image.png?size=large",
		CorrelationID: uuid.NewString(),
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
	bundleID := uuid.NewString()
	lastActivity := timestamppb.New(time.Date(2026, 8, 23, 1, 2, 3, 0, time.FixedZone("KST", 9*60*60)))
	runtime := &fakeMCPFileRuntime{findResult: &managev1.FindMultipartUploadCandidateResponse{
		ClientMediaBundleId: &bundleID,
		FileId:              pointer(fileID), UploadId: pointer("upload-1"), TotalParts: 3, ChunkSize: 1024,
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
		result.Session.LastActivityAt == nil || result.Session.LastActivityAt.Location() != time.UTC ||
		result.Session.Handle.ClientMediaBundleID != bundleID {
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
	bundleID := uuid.NewString()
	delivery := completeDelivery(fileID)
	runtime := &fakeMCPFileRuntime{
		completeResult: &managev1.CompleteMultipartUploadResponse{FileId: fileID, Delivery: delivery},
		deliveryResult: &managev1.GetMediaDeliveryResponse{Delivery: delivery},
	}
	facade, _ := NewMCPFileFacade(runtime)
	handle := MCPFileSessionHandle{
		Transport: MCPFileTransportPresignedMultipart, Kind: MCPFileKindVideo,
		FileID: fileID, UploadID: "upload-1",
		ClientMediaBundleID: bundleID,
	}
	result, err := facade.Complete(context.Background(), handle)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if runtime.completeRequest.GetFileId() != fileID || runtime.completeRequest.GetUploadId() != "upload-1" ||
		runtime.completeRequest.GetClientMediaBundleId() != bundleID {
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

func TestMCPFileCompleteKeepsBundleOptionalAndPreservesMediaPrerequisite(t *testing.T) {
	fileID := uuid.NewString()
	runtime := &fakeMCPFileRuntime{completeResult: &managev1.CompleteMultipartUploadResponse{
		FileId: fileID, Delivery: minimalDelivery(fileID),
	}}
	facade, _ := NewMCPFileFacade(runtime)
	handle := MCPFileSessionHandle{
		Transport: MCPFileTransportPresignedMultipart, Kind: MCPFileKindGeneral,
		FileID: fileID, UploadID: "upload-binary",
	}
	if _, err := facade.Complete(t.Context(), handle); err != nil {
		t.Fatal(err)
	}
	if runtime.completeRequest.ClientMediaBundleId != nil {
		t.Fatalf("completion fabricated a bundle: %+v", runtime.completeRequest)
	}
	prerequisiteError := connect.NewError(connect.CodeFailedPrecondition, errors.New("clientMediaBundleRequired: direct audio and video uploads require completed browser media"))
	runtime.completeError = prerequisiteError
	handle.Kind = MCPFileKindAudio
	if _, err := facade.Complete(t.Context(), handle); !errors.Is(err, prerequisiteError) {
		t.Fatalf("media prerequisite error changed or bypassed: %v", err)
	}
}

func TestMCPFileTrackAudioPreservesNativeTargetCASAndCompletion(t *testing.T) {
	for _, transport := range []MCPFileTransport{MCPFileTransportBrowserUploadPage, MCPFileTransportPresignedMultipart} {
		for _, current := range []string{"", uuid.NewString()} {
			t.Run(string(transport)+"/"+current, func(t *testing.T) {
				fileID, trackID, bundleID := uuid.NewString(), uuid.NewString(), uuid.NewString()
				var expected *string
				if current != "" {
					expected = &current
				}
				runtime := &fakeMCPFileRuntime{
					initiateResult: &managev1.InitiateMultipartUploadResponse{FileId: fileID, UploadId: "track-upload", TotalParts: 1, ChunkSize: 1024, Status: managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_INITIATED},
					findResult:     &managev1.FindMultipartUploadCandidateResponse{FileId: pointer(fileID), UploadId: pointer("track-upload"), TotalParts: 1, ChunkSize: 1024, Status: managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_UPLOADING, ClientMediaBundleId: &bundleID},
					completeResult: &managev1.CompleteMultipartUploadResponse{FileId: fileID, Delivery: minimalDelivery(fileID)},
				}
				facade, _ := NewMCPFileFacade(runtime)
				begun, err := facade.Begin(t.Context(), MCPFileBeginInput{Kind: MCPFileKindTrackAudio, Transport: transport, TrackID: trackID, ExpectedCurrentFileID: expected, FileName: "track.wav", MIMEType: "audio/wav", FileSize: 1024})
				if err != nil {
					t.Fatal(err)
				}
				request := runtime.initiateRequest
				if request.GetUploadType() != managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO || request.GetEntityId() != trackID || request.GetEntityType() != managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK || !reflect.DeepEqual(request.ExpectedCurrentFileId, expected) {
					t.Fatalf("Track initiation target/CAS = %+v", request)
				}
				status, err := facade.Status(t.Context(), begun.Session.Handle)
				if err != nil {
					t.Fatal(err)
				}
				candidate := runtime.findRequest
				if candidate.GetUploadType() != request.UploadType || candidate.GetEntityId() != trackID || candidate.GetEntityType() != request.GetEntityType() || !reflect.DeepEqual(candidate.ExpectedCurrentFileId, expected) || candidate.GetFileId() != fileID || candidate.GetUploadId() != "track-upload" {
					t.Fatalf("Track candidate target/CAS = %+v", candidate)
				}
				if status.Session.Handle.TrackID != trackID || !reflect.DeepEqual(status.Session.Handle.ExpectedCurrentFileID, expected) || status.Session.Handle.ClientMediaBundleID != bundleID {
					t.Fatalf("status lost target/CAS/bundle: %+v", status.Session.Handle)
				}
				if _, err := facade.Complete(t.Context(), status.Session.Handle); err != nil || runtime.completeRequest.GetClientMediaBundleId() != bundleID {
					t.Fatalf("native completion = %+v, %v", runtime.completeRequest, err)
				}
				denied := connect.NewError(connect.CodePermissionDenied, errors.New("Track upload denied"))
				runtime.initiateError = denied
				if _, err := facade.Begin(t.Context(), MCPFileBeginInput{Kind: MCPFileKindTrackAudio, Transport: transport, TrackID: trackID, FileName: "track.wav", MIMEType: "audio/wav", FileSize: 1024}); !errors.Is(err, denied) {
					t.Fatalf("native authority error changed: %v", err)
				}
				stale := connect.NewError(connect.CodeFailedPrecondition, errors.New("Track original audio changed before attachment"))
				runtime.completeError = stale
				if _, err := facade.Complete(t.Context(), status.Session.Handle); !errors.Is(err, stale) {
					t.Fatalf("native attachment CAS error changed: %v", err)
				}
			})
		}
	}
}

func TestMCPFileRemoteImportRequiresAndPreservesDurableCorrelation(t *testing.T) {
	for _, kind := range []MCPFileKind{MCPFileKindGeneral, MCPFileKindImage, MCPFileKindVideo, MCPFileKindAudio, MCPFileKindAttachment, MCPFileKindMesh, MCPFileKindTrackAudio} {
		t.Run(string(kind), func(t *testing.T) {
			fileID := uuid.NewString()
			runtime := &fakeMCPFileRuntime{downloadResult: &managev1.DownloadFromUrlResponse{FileId: fileID, Delivery: minimalDelivery(fileID)}}
			facade, _ := NewMCPFileFacade(runtime)
			input := MCPFileBeginInput{Kind: kind, Transport: MCPFileTransportRemoteHTTPS, RemoteURL: "https://example.com/audio.wav"}
			if kind == MCPFileKindTrackAudio {
				input.TrackID, input.ExpectedCurrentFileID = uuid.NewString(), pointer(uuid.NewString())
			}
			_, err := facade.Begin(t.Context(), input)
			if kind == MCPFileKindGeneral {
				if err != nil || runtime.downloadRequest.CorrelationId != nil {
					t.Fatalf("general import changed: %+v, %v", runtime.downloadRequest, err)
				}
			} else if !errors.Is(err, ErrInvalidMCPFileInput) || runtime.downloadRequest != nil {
				t.Fatalf("missing durable correlation called native owner: %+v, %v", runtime.downloadRequest, err)
			}
			input.CorrelationID = uuid.NewString()
			for attempt := 0; attempt < 2; attempt++ {
				if _, err := facade.Begin(t.Context(), input); err != nil {
					t.Fatal(err)
				}
				request := runtime.downloadRequest
				if request.GetCorrelationId() != input.CorrelationID || request.GetEntityId() != input.TrackID || !reflect.DeepEqual(request.ExpectedCurrentFileId, input.ExpectedCurrentFileID) {
					t.Fatalf("retry changed import identity: %+v", request)
				}
				if kind == MCPFileKindTrackAudio && (request.UploadType != managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO || request.GetEntityType() != managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK) {
					t.Fatalf("remote Track target = %+v", request)
				}
			}
		})
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
