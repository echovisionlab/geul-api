package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mediaauth"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/adapters/filemedia"
	"github.com/echovisionlab/geul-api/internal/auth"
	filemediadomain "github.com/echovisionlab/geul-api/internal/filemedia"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type recordingMCPFileRuntime struct {
	importInput *filemediadomain.RemoteFileImportInput
	user        *auth.UserInfo

	initiateRequest *managev1.InitiateMultipartUploadRequest
	initiateResult  *filemediadomain.MultipartUpload
	initiateError   error

	findRequest *managev1.FindMultipartUploadCandidateRequest
	findResult  *filemediadomain.MultipartCandidate
	findError   error

	completeRequest *managev1.CompleteMultipartUploadRequest
	completeResult  *filemediadomain.CompletedUpload
	completeError   error

	downloadRequest *managev1.DownloadFromUrlRequest
	downloadResult  *filemediadomain.ImportedFile
	downloadError   error

	deliveryRequest *managev1.GetMediaDeliveryRequest
	deliveryResult  *filemediadomain.MediaDeliveryResult
	deliveryError   error

	presignCommand *filemediadomain.PresignMultipartPartsCommand
	presignResult  []filemediadomain.PresignedUploadPart
	abortCommand   *filemediadomain.AbortMultipartUploadCommand
}

func (runtime *recordingMCPFileRuntime) PresignMultipartParts(ctx context.Context, command filemediadomain.PresignMultipartPartsCommand) ([]filemediadomain.PresignedUploadPart, error) {
	runtime.capture(ctx)
	runtime.presignCommand = &command
	return runtime.presignResult, nil
}

func (runtime *recordingMCPFileRuntime) AbortMultipartUpload(ctx context.Context, command filemediadomain.AbortMultipartUploadCommand) error {
	runtime.capture(ctx)
	runtime.abortCommand = &command
	return nil
}

func (runtime *recordingMCPFileRuntime) ImportRemoteFile(ctx context.Context, input filemediadomain.RemoteFileImportInput) (*filemediadomain.ImportedFile, error) {
	runtime.capture(ctx)
	runtime.importInput = &input
	return runtime.downloadResult, runtime.downloadError
}

func (runtime *recordingMCPFileRuntime) capture(ctx context.Context) {
	runtime.user = auth.GetUser(ctx)
}

// The fake records each command as the request a Connect client would have
// sent, so the assertions read the native field names.
func (runtime *recordingMCPFileRuntime) InitiateMultipartUpload(ctx context.Context, command filemediadomain.InitiateMultipartUploadCommand) (*filemediadomain.MultipartUpload, error) {
	runtime.capture(ctx)
	runtime.initiateRequest = &managev1.InitiateMultipartUploadRequest{
		UploadType: managev1.UploadType(command.UploadType), EntityId: command.EntityID, FileSize: command.FileSize,
		MimeType: command.MimeType, EntityType: (*managev1.TranscodeEntityType)(command.EntityType), FileName: command.FileName,
		FileLastModified: command.FileLastModified, SlotId: command.SlotID, ExpectedCurrentFileId: command.ExpectedCurrentFileID,
	}
	if runtime.initiateError != nil {
		return nil, runtime.initiateError
	}
	return runtime.initiateResult, nil
}

func (runtime *recordingMCPFileRuntime) FindMultipartUploadCandidate(ctx context.Context, query filemediadomain.FindMultipartUploadCandidateQuery) (*filemediadomain.MultipartCandidate, error) {
	runtime.capture(ctx)
	runtime.findRequest = &managev1.FindMultipartUploadCandidateRequest{
		UploadType: managev1.UploadType(query.UploadType), EntityId: query.EntityID, FileId: query.FileID, FileName: query.FileName,
		FileSize: query.FileSize, MimeType: query.MimeType, FileLastModified: query.FileLastModified,
		EntityType: (*managev1.TranscodeEntityType)(query.EntityType), SlotId: query.SlotID, UploadId: query.UploadID, ExpectedCurrentFileId: query.ExpectedCurrentFileID,
	}
	if runtime.findError != nil {
		return nil, runtime.findError
	}
	return runtime.findResult, nil
}

func (runtime *recordingMCPFileRuntime) CompleteMultipartUpload(ctx context.Context, command filemediadomain.CompleteMultipartUploadCommand) (*filemediadomain.CompletedUpload, error) {
	runtime.capture(ctx)
	runtime.completeRequest = &managev1.CompleteMultipartUploadRequest{
		FileId: command.FileID, UploadId: command.UploadID,
		CorrelationId: optionalRecordedString(command.CorrelationID), ClientMediaBundleId: optionalRecordedString(command.ClientMediaBundleID),
	}
	if runtime.completeError != nil {
		return nil, runtime.completeError
	}
	return runtime.completeResult, nil
}

func (runtime *recordingMCPFileRuntime) DownloadFromURL(ctx context.Context, command filemediadomain.DownloadFromURLCommand) (*filemediadomain.ImportedFile, error) {
	runtime.capture(ctx)
	runtime.downloadRequest = &managev1.DownloadFromUrlRequest{
		UploadType: managev1.UploadType(command.UploadType), EntityId: command.EntityID, Url: command.URL, EntityType: (*managev1.TranscodeEntityType)(command.EntityType),
		CorrelationId: optionalRecordedString(command.CorrelationID), SlotId: optionalRecordedString(command.SlotID),
		ExpectedCurrentFileId: command.ExpectedCurrentFileID,
	}
	if runtime.downloadError != nil {
		return nil, runtime.downloadError
	}
	return runtime.downloadResult, nil
}

func (runtime *recordingMCPFileRuntime) GetMediaDelivery(ctx context.Context, query filemediadomain.GetMediaDeliveryQuery) (*filemediadomain.MediaDeliveryResult, error) {
	runtime.capture(ctx)
	runtime.deliveryRequest = &managev1.GetMediaDeliveryRequest{FileId: query.FileID}
	if runtime.deliveryError != nil {
		return nil, runtime.deliveryError
	}
	return runtime.deliveryResult, nil
}

func optionalRecordedString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func TestFileToolsExposeCompactReferenceOnlySurface(t *testing.T) {
	tools := mustFileTools(t, &recordingMCPFileRuntime{})
	listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if got, want := tools.ToolNames(), []string{ToolFileTransfer, ToolFileRead, ToolFileUpload}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ToolNames() = %v, want %v", got, want)
	}
	if len(listed) != 3 {
		t.Fatalf("ListTools() returned %d tools", len(listed))
	}
	wantAnnotations := map[string]map[string]any{
		ToolFileTransfer: toolAnnotations(false, false, true),
		ToolFileRead:     toolAnnotations(true, false, false),
		ToolFileUpload:   toolAnnotations(false, false, true),
	}
	for _, tool := range listed {
		assertMCPToolOAuthSecurity(t, tool)
		assertMCPToolAnnotations(t, tool, wantAnnotations[tool.Name])
		for schemaName, schema := range map[string]json.RawMessage{"input": tool.InputSchema, "output": tool.OutputSchema} {
			var object map[string]any
			if err := json.Unmarshal(schema, &object); err != nil || object["type"] != "object" {
				t.Fatalf("%s %s schema = %s (%v)", tool.Name, schemaName, schema, err)
			}
			lower := strings.ToLower(string(schema))
			for _, forbidden := range []string{"base64", "bytes", "blob"} {
				if strings.Contains(lower, `"`+forbidden+`":`) {
					t.Fatalf("%s %s schema exposed payload field %q", tool.Name, schemaName, forbidden)
				}
			}
			if strings.Contains(lower, "data:") {
				t.Fatalf("%s %s schema exposed a data URI", tool.Name, schemaName)
			}
		}
	}

	listed[0].InputSchema[0] = '['
	listed[0].SecuritySchemes[0].Scopes[0] = "other"
	listed[0].Meta["securitySchemes"].([]mcpserver.ToolSecurityScheme)[0].Scopes[0] = "other"
	listed[0].Annotations["readOnlyHint"] = true
	again, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	if err != nil || again[0].InputSchema[0] != '{' ||
		!reflect.DeepEqual(again[0].SecuritySchemes[0].Scopes, []string{"mcp", "offline_access"}) ||
		!reflect.DeepEqual(again[0].Meta["securitySchemes"].([]mcpserver.ToolSecurityScheme)[0].Scopes, []string{"mcp", "offline_access"}) ||
		again[0].Annotations["readOnlyHint"] != false {
		t.Fatal("ListTools() returned mutable shared definitions")
	}
	if _, err := NewFileTools(nil); err == nil {
		t.Fatal("NewFileTools(nil) succeeded")
	}
}

func TestFileToolsTransferActionsUseOneSession(t *testing.T) {
	fileID := uuid.NewString()
	bundleID := uuid.NewString()
	lastActivity := time.Date(2026, 8, 23, 10, 11, 12, 123, time.FixedZone("test", 9*60*60))
	runtime := &recordingMCPFileRuntime{
		initiateResult: &filemediadomain.MultipartUpload{
			FileID: fileID, UploadID: "upload-a", TotalParts: 3, ChunkSize: 1024,
			Status: filemediadomain.UploadSessionStatusInitiated,
		},
		findResult: &filemediadomain.MultipartCandidate{
			ClientMediaBundleID: &bundleID,
			FileID:              fileStringPointer(fileID), UploadID: fileStringPointer("upload-a"),
			FileName: fileStringPointer("audio.wav"), MimeType: fileStringPointer("audio/wav"), FileSize: 3072,
			TotalParts: 3, ChunkSize: 1024,
			Status:         filemediadomain.UploadSessionStatusUploading,
			UploadedParts:  []*filemediadomain.UploadedPart{{PartNumber: 1, ETag: "must-not-leak"}, {PartNumber: 3, ETag: "must-not-leak"}},
			LastActivityAt: &lastActivity,
		},
	}
	tools := mustFileTools(t, runtime)

	begin, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileTransfer, fileToolArguments(t,
		`{"action":"begin","kind":"audio","transport":"presigned_multipart","file_name":"audio.wav","mime_type":"audio/wav","file_size":3072,"last_modified_ms":123}`))
	if err != nil {
		t.Fatalf("file_transfer(begin) error = %v", err)
	}
	if request := runtime.initiateRequest; request == nil || request.GetFileName() != "audio.wav" ||
		request.GetMimeType() != "audio/wav" || request.GetFileSize() != 3072 || request.GetFileLastModified() != 123 ||
		request.GetUploadType() != managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO {
		t.Fatalf("initiate request = %+v", request)
	}
	session := begin.StructuredContent["upload"].(map[string]any)
	handle := session["session"].(map[string]any)
	if !reflect.DeepEqual(handle, map[string]any{"transport": "presigned_multipart", "kind": "audio", "file_id": fileID, "upload_id": "upload-a"}) || session["state"] != "initiated" {
		t.Fatalf("begin session = %+v", session)
	}
	if strings.Contains(begin.Content[0]["text"].(string), "must-not-leak") {
		t.Fatalf("begin result leaked runtime-only value: %+v", begin)
	}

	statusArguments := `{"action":"status","session":{"transport":"presigned_multipart","kind":"audio","file_id":"` + fileID + `","upload_id":"upload-a"}}`
	status, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileTransfer, fileToolArguments(t, statusArguments))
	if err != nil {
		t.Fatalf("file_transfer(status) error = %v", err)
	}
	if request := runtime.findRequest; request == nil || request.GetFileId() != fileID || request.GetUploadId() != "upload-a" ||
		request.GetUploadType() != managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO {
		t.Fatalf("status request = %+v", request)
	}
	statusText := status.Content[0]["text"].(string)
	statusSession := status.StructuredContent["upload"].(map[string]any)
	if statusSession["client_media_bundle_id"] != bundleID || !reflect.DeepEqual(statusSession["session"], handle) {
		t.Fatalf("status changed the four-slot handle or lost bundle metadata: %+v", statusSession)
	}
	if !strings.Contains(statusText, `"uploaded_parts":[1,3]`) || !strings.Contains(statusText, `"last_activity_at":"2026-08-23T01:11:12.000000123Z"`) ||
		strings.Contains(statusText, "must-not-leak") {
		t.Fatalf("status result = %s", statusText)
	}
}

func TestFileToolsPresignedTransferNeedsOnlyBeginAndComplete(t *testing.T) {
	fileID := uuid.NewString()
	expires := time.Date(2026, 11, 6, 1, 0, 0, 0, time.UTC)
	runtime := &recordingMCPFileRuntime{
		initiateResult: &filemediadomain.MultipartUpload{
			FileID: fileID, UploadID: "upload-i", TotalParts: 2, ChunkSize: 1024,
			Status: filemediadomain.UploadSessionStatusInitiated,
		},
		presignResult: []filemediadomain.PresignedUploadPart{
			{PartNumber: 1, URL: "https://s3.example/1", Size: 1024, ExpiresAt: expires},
			{PartNumber: 2, URL: "https://s3.example/2", Size: 10, ExpiresAt: expires},
		},
		findResult: &filemediadomain.MultipartCandidate{
			FileID: fileStringPointer(fileID), UploadID: fileStringPointer("upload-i"), FileSize: 1034, TotalParts: 2, ChunkSize: 1024,
			Status: filemediadomain.UploadSessionStatusUploading,
		},
		completeResult: &filemediadomain.CompletedUpload{FileID: fileID, Delivery: fileTestDelivery(fileID)},
	}
	tools := mustFileTools(t, runtime)

	begin, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileTransfer, fileToolArguments(t,
		`{"action":"begin","kind":"image","transport":"presigned_multipart","file_name":"icon.png","mime_type":"image/png","file_size":1034}`))
	if err != nil {
		t.Fatalf("file_transfer(begin) error = %v", err)
	}
	session := begin.StructuredContent["upload"].(map[string]any)
	wantParts := []any{
		map[string]any{"part_number": float64(1), "url": "https://s3.example/1", "size": float64(1024)},
		map[string]any{"part_number": float64(2), "url": "https://s3.example/2", "size": float64(10)},
	}
	if !reflect.DeepEqual(session["part_uploads"], wantParts) || session["part_uploads_expire_at"] != "2026-11-06T01:00:00Z" {
		t.Fatalf("begin upload URLs = %v, expiry %v", session["part_uploads"], session["part_uploads_expire_at"])
	}
	if runtime.presignCommand == nil || runtime.presignCommand.FileID != fileID || runtime.presignCommand.UploadID != "upload-i" {
		t.Fatalf("presign command = %+v", runtime.presignCommand)
	}

	handle := `{"transport":"presigned_multipart","kind":"image","file_id":"` + fileID + `","upload_id":"upload-i"}`
	if _, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileTransfer, fileToolArguments(t, `{"action":"complete","session":`+handle+`}`)); err != nil {
		t.Fatalf("file_transfer(complete) error = %v", err)
	}
	if runtime.findRequest == nil || runtime.completeRequest == nil {
		t.Fatalf("complete must reconcile parts first: find %+v, complete %+v", runtime.findRequest, runtime.completeRequest)
	}

	parts, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileTransfer, fileToolArguments(t, `{"action":"parts","session":`+handle+`}`))
	if err != nil {
		t.Fatalf("file_transfer(parts) error = %v", err)
	}
	if got := parts.StructuredContent["upload"].(map[string]any)["part_uploads"]; !reflect.DeepEqual(got, wantParts) {
		t.Fatalf("parts upload URLs = %v", got)
	}

	aborted, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileTransfer, fileToolArguments(t, `{"action":"abort","session":`+handle+`}`))
	if err != nil {
		t.Fatalf("file_transfer(abort) error = %v", err)
	}
	if aborted.StructuredContent["state"] != "aborted" || runtime.abortCommand == nil || runtime.abortCommand.UploadID != "upload-i" {
		t.Fatalf("abort = %+v, command %+v", aborted.StructuredContent, runtime.abortCommand)
	}
}

func TestFileToolsPresignedAudioGetsNoUploadURLs(t *testing.T) {
	runtime := &recordingMCPFileRuntime{initiateResult: &filemediadomain.MultipartUpload{
		FileID: uuid.NewString(), UploadID: "upload-a", TotalParts: 1, ChunkSize: 1024,
		Status: filemediadomain.UploadSessionStatusInitiated,
	}}
	begin, err := mustFileTools(t, runtime).CallTool(t.Context(), mcpserver.Principal{}, ToolFileTransfer, fileToolArguments(t,
		`{"action":"begin","kind":"general","transport":"presigned_multipart","file_name":"a.wav","mime_type":"audio/wav","file_size":10}`))
	if err != nil {
		t.Fatalf("file_transfer(begin) error = %v", err)
	}
	if runtime.presignCommand != nil || begin.StructuredContent["upload"].(map[string]any)["part_uploads"] != nil {
		t.Fatalf("direct audio must keep the browser flow: %+v", begin.StructuredContent)
	}
}

func TestFileToolsCompleteRemoteAndReadReturnVerifiedReferences(t *testing.T) {
	fileID := uuid.NewString()
	bundleID := uuid.NewString()
	delivery := fileTestDelivery(fileID)
	runtime := &recordingMCPFileRuntime{
		completeResult: &filemediadomain.CompletedUpload{FileID: fileID, Delivery: delivery},
		downloadResult: &filemediadomain.ImportedFile{FileID: fileID, Delivery: delivery},
		deliveryResult: &filemediadomain.MediaDeliveryResult{Delivery: delivery},
	}
	tools := mustFileTools(t, runtime)

	completeArguments := `{"action":"complete","session":{"transport":"browser_upload_page","kind":"video","file_id":"` + fileID + `","upload_id":"upload-v"},"client_media_bundle_id":"` + bundleID + `"}`
	completed, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileTransfer, fileToolArguments(t, completeArguments))
	if err != nil {
		t.Fatalf("file_transfer(complete) error = %v", err)
	}
	if runtime.completeRequest.GetFileId() != fileID || runtime.completeRequest.GetUploadId() != "upload-v" ||
		runtime.completeRequest.GetClientMediaBundleId() != bundleID {
		t.Fatalf("complete request = %+v", runtime.completeRequest)
	}
	if completed.StructuredContent["state"] != "ready" || !strings.Contains(completed.Content[0]["text"].(string), `"references":[{"kind":"inline"`) {
		t.Fatalf("complete result = %+v", completed)
	}

	remote, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileTransfer, fileToolArguments(t,
		`{"action":"begin","kind":"video","transport":"remote_https","remote_url":"https://example.com/video.mp4","correlation_id":"`+uuid.NewString()+`"}`))
	if err != nil {
		t.Fatalf("file_transfer(remote begin) error = %v", err)
	}
	if runtime.downloadRequest.GetUrl() != "https://example.com/video.mp4" || remote.StructuredContent["state"] != "ready" {
		t.Fatalf("remote result/request = %+v / %+v", remote, runtime.downloadRequest)
	}

	read, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileRead, fileToolArguments(t, `{"file_id":"`+fileID+`"}`))
	if err != nil {
		t.Fatalf("file_read error = %v", err)
	}
	if runtime.deliveryRequest.GetFileId() != fileID || read.StructuredContent["file_id"] != fileID ||
		strings.Contains(read.Content[0]["text"].(string), "sha256-must-not-leak") {
		t.Fatalf("file_read result/request = %+v / %+v", read, runtime.deliveryRequest)
	}
}

func TestFileToolsTrackAudioHandleRoundTripPreservesTargetAndCAS(t *testing.T) {
	for _, current := range []string{"", uuid.NewString()} {
		t.Run(current, func(t *testing.T) {
			fileID, trackID, bundleID := uuid.NewString(), uuid.NewString(), uuid.NewString()
			runtime := &recordingMCPFileRuntime{
				initiateResult: &filemediadomain.MultipartUpload{FileID: fileID, UploadID: "track-upload", TotalParts: 1, ChunkSize: 1024, Status: filemediadomain.UploadSessionStatusInitiated},
				findResult:     &filemediadomain.MultipartCandidate{FileID: fileStringPointer(fileID), UploadID: fileStringPointer("track-upload"), TotalParts: 1, ChunkSize: 1024, Status: filemediadomain.UploadSessionStatusUploading, ClientMediaBundleID: &bundleID},
				completeResult: &filemediadomain.CompletedUpload{FileID: fileID, Delivery: fileTestDelivery(fileID)},
			}
			tools := mustFileTools(t, runtime)
			arguments := `{"action":"begin","kind":"track_audio","transport":"browser_upload_page","track_id":"` + trackID + `","file_name":"track.wav","mime_type":"audio/wav","file_size":1024`
			if current != "" {
				arguments += `,"expected_current_file_id":"` + current + `"`
			}
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileTransfer, fileToolArguments(t, arguments+`}`))
			if err != nil {
				t.Fatal(err)
			}
			handle := result.StructuredContent["upload"].(map[string]any)["session"]
			want := map[string]any{"transport": "browser_upload_page", "kind": "track_audio", "file_id": fileID, "upload_id": "track-upload", "track_id": trackID}
			if current != "" {
				want["expected_current_file_id"] = current
			}
			if !reflect.DeepEqual(handle, want) || runtime.initiateRequest.GetEntityId() != trackID || runtime.initiateRequest.GetExpectedCurrentFileId() != current {
				t.Fatalf("begin target/CAS = %+v, %+v", handle, runtime.initiateRequest)
			}
			encodedHandle, _ := json.Marshal(handle)
			status, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileTransfer, fileToolArguments(t, `{"action":"status","session":`+string(encodedHandle)+`}`))
			if err != nil {
				t.Fatal(err)
			}
			statusSession := status.StructuredContent["upload"].(map[string]any)
			if !reflect.DeepEqual(statusSession["session"], want) || runtime.findRequest.GetEntityId() != trackID || runtime.findRequest.GetExpectedCurrentFileId() != current || (runtime.findRequest.ExpectedCurrentFileId == nil) != (current == "") {
				t.Fatalf("status lost target/CAS = %+v, %+v", statusSession, runtime.findRequest)
			}
			_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileTransfer, fileToolArguments(t, `{"action":"complete","session":`+string(encodedHandle)+`,"client_media_bundle_id":"`+bundleID+`"}`))
			if err != nil || runtime.completeRequest.GetFileId() != fileID || runtime.completeRequest.GetClientMediaBundleId() != bundleID {
				t.Fatalf("completion bypassed native session: %+v, %v", runtime.completeRequest, err)
			}
		})
	}
}

func TestFileToolsRemoteTrackAudioForwardsStableRetryIdentity(t *testing.T) {
	fileID, trackID, current, correlationID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	runtime := &recordingMCPFileRuntime{downloadResult: &filemediadomain.ImportedFile{FileID: fileID, Delivery: fileTestDelivery(fileID)}}
	tools := mustFileTools(t, runtime)
	arguments := fileToolArguments(t, `{"action":"begin","kind":"track_audio","transport":"remote_https","track_id":"`+trackID+`","expected_current_file_id":"`+current+`","correlation_id":"`+correlationID+`","remote_url":"https://example.com/track.wav"}`)
	for attempt := 0; attempt < 2; attempt++ {
		result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileTransfer, arguments)
		if err != nil || result.IsError || runtime.downloadRequest.GetEntityId() != trackID || runtime.downloadRequest.GetExpectedCurrentFileId() != current || runtime.downloadRequest.GetCorrelationId() != correlationID || runtime.downloadRequest.GetEntityType() != managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK {
			t.Fatalf("remote retry target/identity = %+v, %+v, %v", runtime.downloadRequest, result, err)
		}
	}
}

func TestFileToolsRejectInvalidTrackAudioTargetsBeforeNativeCall(t *testing.T) {
	fileID, trackID := uuid.NewString(), uuid.NewString()
	for _, arguments := range []string{
		`{"action":"begin","kind":"track_audio","transport":"presigned_multipart","file_name":"track.wav","mime_type":"audio/wav","file_size":1024}`,
		`{"action":"begin","kind":"audio","transport":"presigned_multipart","track_id":"` + trackID + `","file_name":"track.wav","mime_type":"audio/wav","file_size":1024}`,
		`{"action":"begin","kind":"track_audio","transport":"presigned_multipart","track_id":"` + trackID + `","expected_current_file_id":"","file_name":"track.wav","mime_type":"audio/wav","file_size":1024}`,
		`{"action":"begin","kind":"track_audio","transport":"presigned_multipart","track_id":"` + trackID + `","expected_current_file_id":null,"file_name":"track.wav","mime_type":"audio/wav","file_size":1024}`,
		`{"action":"begin","kind":"track_audio","transport":"remote_https","track_id":"` + trackID + `","remote_url":"https://example.com/track.wav"}`,
		`{"action":"begin","kind":"audio","transport":"remote_https","correlation_id":"bad","remote_url":"https://example.com/track.wav"}`,
		`{"action":"status","session":{"transport":"presigned_multipart","kind":"track_audio","file_id":"` + fileID + `","upload_id":"upload"}}`,
		`{"action":"status","session":{"transport":"presigned_multipart","kind":"audio","file_id":"` + fileID + `","upload_id":"upload","track_id":"` + trackID + `"}}`,
		`{"action":"status","session":{"transport":"presigned_multipart","kind":"track_audio","file_id":"` + fileID + `","upload_id":"upload","track_id":"` + trackID + `","expected_current_file_id":"bad"}}`,
	} {
		runtime := &recordingMCPFileRuntime{}
		tools := mustFileTools(t, runtime)
		result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileTransfer, fileToolArguments(t, arguments))
		var executionErr *mcpserver.ToolExecutionError
		if !errors.As(err, &executionErr) || runtime.initiateRequest != nil || runtime.findRequest != nil || runtime.downloadRequest != nil {
			t.Fatalf("invalid target reached owner: %s / %+v / %v", arguments, result, err)
		}
	}
}

func TestFileToolsRejectInlineOrActionIncompatibleArguments(t *testing.T) {
	fileID := uuid.NewString()
	tools := mustFileTools(t, &recordingMCPFileRuntime{})
	tests := []struct {
		name      string
		tool      string
		arguments string
	}{
		{name: "missing action", tool: ToolFileTransfer, arguments: `{}`},
		{name: "unknown action", tool: ToolFileTransfer, arguments: `{"action":"upload"}`},
		{name: "inline field", tool: ToolFileTransfer, arguments: `{"action":"begin","kind":"image","transport":"remote_https","remote_url":"https://example.com/a.png","base64":"AA=="}`},
		{name: "data URL", tool: ToolFileTransfer, arguments: `{"action":"begin","kind":"image","transport":"remote_https","remote_url":"data:image/png,AA"}`},
		{name: "remote metadata", tool: ToolFileTransfer, arguments: `{"action":"begin","kind":"image","transport":"remote_https","remote_url":"https://example.com/a.png","file_name":"a.png"}`},
		{name: "short handle", tool: ToolFileTransfer, arguments: `{"action":"status","session":["presigned_multipart","image"]}`},
		{name: "status extra field", tool: ToolFileTransfer, arguments: `{"action":"status","session":{"transport":"presigned_multipart","kind":"image","file_id":"` + fileID + `","upload_id":"upload"},"file_id":"` + fileID + `"}`},
		{name: "status rejects completion bundle", tool: ToolFileTransfer, arguments: `{"action":"status","session":{"transport":"presigned_multipart","kind":"image","file_id":"` + fileID + `","upload_id":"upload"},"client_media_bundle_id":"` + uuid.NewString() + `"}`},
		{name: "read inline field", tool: ToolFileRead, arguments: `{"file_id":"` + fileID + `","bytes":"AA=="}`},
		{name: "read invalid ID", tool: ToolFileRead, arguments: `{"file_id":"not-a-uuid"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, test.tool, fileToolArguments(t, test.arguments))
			var executionErr *mcpserver.ToolExecutionError
			if !errors.As(err, &executionErr) {
				t.Fatalf("CallTool() error = %T %v, want ToolExecutionError", err, err)
			}
		})
	}
}

func TestFileToolsPreserveFacadeAuthorityAndInternalErrorBoundaries(t *testing.T) {
	fileID := uuid.NewString()
	runtime := &recordingMCPFileRuntime{}
	tools := mustFileTools(t, runtime)
	arguments := fileToolArguments(t, `{"file_id":"`+fileID+`"}`)

	runtime.deliveryError = connect.NewError(connect.CodePermissionDenied, errors.New("denied"))
	_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileRead, arguments)
	var executionErr *mcpserver.ToolExecutionError
	if !errors.As(err, &executionErr) {
		t.Fatalf("permission error = %T %v", err, err)
	}

	runtime.deliveryError = nil
	runtime.deliveryResult = &filemediadomain.MediaDeliveryResult{}
	_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileRead, arguments)
	if !errors.Is(err, filemedia.ErrInvalidMCPFileRuntime) || errors.As(err, &executionErr) {
		t.Fatalf("runtime error = %T %v", err, err)
	}

	internalErr := connect.NewError(connect.CodeInternal, errors.New("internal detail"))
	runtime.deliveryError = internalErr
	_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileRead, arguments)
	if !errors.Is(err, internalErr) || errors.As(err, &executionErr) {
		t.Fatalf("internal error = %T %v", err, err)
	}
}

func TestFileToolsUseWrappedAuthenticatedContext(t *testing.T) {
	fileID := uuid.NewString()
	runtime := &recordingMCPFileRuntime{
		deliveryResult: &filemediadomain.MediaDeliveryResult{Delivery: fileTestDelivery(fileID)},
	}
	tools := mustFileTools(t, runtime)
	dispatcher, err := WrapDispatcher(tools)
	if err != nil {
		t.Fatalf("WrapDispatcher() error = %v", err)
	}
	principal := validMCPPrincipal()
	if _, err := dispatcher.CallTool(t.Context(), principal, ToolFileRead, fileToolArguments(t, `{"file_id":"`+fileID+`"}`)); err != nil {
		t.Fatalf("wrapped file_read error = %v", err)
	}
	if runtime.user == nil || runtime.user.IdentityID.String() != principal.IdentityID ||
		runtime.user.MemberID.String() != principal.MemberID || runtime.user.SessionID != "" {
		t.Fatalf("File facade auth context = %+v", runtime.user)
	}

	if _, err := tools.CallTool(t.Context(), principal, "file_unknown", nil); !errors.Is(err, mcpserver.ErrUnknownTool) {
		t.Fatalf("unknown tool error = %v", err)
	}
}

func mustFileTools(t *testing.T, runtime *recordingMCPFileRuntime) *FileTools {
	t.Helper()
	facade, err := filemedia.NewMCPFileFacade(runtime)
	if err != nil {
		t.Fatalf("NewMCPFileFacade() error = %v", err)
	}
	tools, err := NewFileTools(facade)
	if err != nil {
		t.Fatalf("NewFileTools() error = %v", err)
	}
	return tools
}

func fileToolArguments(t *testing.T, value string) mcpserver.ToolArguments {
	t.Helper()
	var arguments mcpserver.ToolArguments
	if err := json.Unmarshal([]byte(value), &arguments); err != nil {
		t.Fatalf("decode test arguments: %v", err)
	}
	return arguments
}

func fileTestDelivery(fileID string) *filemediadomain.Delivery {
	fileName := "video.mp4"
	expiresAt := time.Date(2026, 8, 23, 11, 12, 13, 0, time.UTC)
	return &filemediadomain.Delivery{
		FileID: fileID, FileName: &fileName, Extension: "mp4", MimeType: "video/mp4", FileSize: 42,
		Inline: &mediaauth.Reference{
			FileID: fileID, URL: "https://files.example.com/inline", ExpiresAt: expiresAt,
			Extension: "mp4", MimeType: "video/mp4", FileName: &fileName,
		},
		Asset: &mediaasset.Reference{
			AssetID: "asset-a", URL: "https://cdn.example.com/video.mp4", Extension: "mp4",
			MimeType: "video/mp4", FileSize: 42, SHA256: []byte("sha256-must-not-leak"), DownloadFilename: &fileName,
		},
		ProcessingStatus: filemediadomain.ProcessingReady,
	}
}

func fileStringPointer(value string) *string { return &value }

func TestFileToolsUploadAttachmentDescriptorAndStrictMapping(t *testing.T) {
	fileID, correlationID := uuid.NewString(), uuid.NewString()
	runtime := &recordingMCPFileRuntime{downloadResult: &filemediadomain.ImportedFile{FileID: fileID, Delivery: fileTestDelivery(fileID)}}
	tools := mustFileTools(t, runtime)
	listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	if err != nil {
		t.Fatal(err)
	}
	upload := listed[2]
	if upload.Name != ToolFileUpload || !reflect.DeepEqual(upload.Meta["openai/fileParams"], []string{"file"}) {
		t.Fatalf("attachment descriptor = %+v", upload)
	}
	upload.Meta["openai/fileParams"].([]string)[0] = "changed"
	again, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	if err != nil || !reflect.DeepEqual(again[2].Meta["openai/fileParams"], []string{"file"}) {
		t.Fatal("shared attachment metadata was mutable")
	}
	input := `{"file":{"download_url":"https://attachments.example.com/download?signature=private","file_id":"file-opaque-chatgpt-id","file_name":"Original.mp4","mime_type":"image/png"},"kind":"video","correlation_id":"` + correlationID + `"}`
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileUpload, fileToolArguments(t, input))
	if err != nil {
		t.Fatal(err)
	}
	want := filemediadomain.RemoteFileImportInput{UploadType: filemediadomain.
		UploadEditorVideo, SourceURL: "https://attachments.example.com/download?signature=private", FileName: "Original.mp4", CorrelationID: correlationID}
	if runtime.importInput == nil || !reflect.DeepEqual(*runtime.importInput, want) || runtime.downloadRequest != nil {
		t.Fatalf("native input = %+v", runtime.importInput)
	}
	if result.StructuredContent["file_id"] != fileID || result.StructuredContent["mime_type"] != "video/mp4" || strings.Contains(result.Content[0]["text"].(string), "private") || strings.Contains(result.Content[0]["text"].(string), "opaque") {
		t.Fatalf("verified attachment result = %+v", result)
	}
	for _, invalid := range []string{
		`{"file":null,"kind":"general","correlation_id":"` + correlationID + `"}`,
		`{"file":{"download_url":"https://example.com/file","file_id":"opaque","bytes":"x"},"kind":"general","correlation_id":"` + correlationID + `"}`,
		`{"file":{"download_url":"https://example.com/file","file_id":"opaque"},"kind":"track_audio","correlation_id":"` + correlationID + `"}`,
		`{"file":{"download_url":"http://example.com/file","file_id":"opaque"},"kind":"general","correlation_id":"` + correlationID + `"}`,
		`{"file":{"download_url":"https://example.com/file"},"kind":"general","correlation_id":"` + correlationID + `"}`,
		`{"file":{"download_url":"https://example.com/file","file_id":"opaque"},"kind":"general","correlation_id":"not-a-uuid"}`,
		`{"file":{"download_url":"https://example.com/file","file_id":"opaque"},"kind":"general","correlation_id":"` + correlationID + `","post_id":"` + fileID + `"}`,
	} {
		runtime.importInput = nil
		_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileUpload, fileToolArguments(t, invalid))
		var execution *mcpserver.ToolExecutionError
		if !errors.As(err, &execution) || runtime.importInput != nil {
			t.Fatalf("invalid input reached authority: %s / %v", invalid, err)
		}
	}
}

func TestFileTransferInputSchemaIsOneFlatObject(t *testing.T) {
	var schema map[string]json.RawMessage
	if err := json.Unmarshal(fileTransferInputSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	for _, keyword := range []string{"allOf", "anyOf", "oneOf", "not", "if", "then", "else", "dependentRequired", "dependentSchemas"} {
		if _, ok := schema[keyword]; ok {
			t.Fatalf("file_transfer input schema root uses %q", keyword)
		}
	}
	if string(schema["type"]) != `"object"` || string(schema["additionalProperties"]) != "false" || string(schema["required"]) != `["action"]` {
		t.Fatalf("file_transfer input schema root = type %s, additionalProperties %s, required %s", schema["type"], schema["additionalProperties"], schema["required"])
	}
	var properties map[string]struct {
		Enum        []string `json:"enum"`
		Description string   `json:"description"`
	}
	if err := json.Unmarshal(schema["properties"], &properties); err != nil {
		t.Fatal(err)
	}
	want := []string{"action", "kind", "transport", "file_name", "mime_type", "file_size", "last_modified_ms", "remote_url", "track_id", "expected_current_file_id", "correlation_id", "session", "client_media_bundle_id"}
	if len(properties) != len(want) {
		t.Fatalf("file_transfer input properties = %d, want %d", len(properties), len(want))
	}
	for _, name := range want {
		property, ok := properties[name]
		if !ok || property.Description == "" {
			t.Fatalf("file_transfer input property %q missing or undocumented", name)
		}
	}
	if !reflect.DeepEqual(properties["action"].Enum, []string{"begin", "status", "parts", "complete", "abort"}) ||
		!reflect.DeepEqual(properties["transport"].Enum, []string{"browser_upload_page", "presigned_multipart", "remote_https"}) {
		t.Fatalf("file_transfer action/transport enums = %v / %v", properties["action"].Enum, properties["transport"].Enum)
	}
}

func TestFileToolsTransferEnforcesFlattenedSchemaRules(t *testing.T) {
	fileID, trackID, current, correlationID, bundleID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	handle := `{"transport":"presigned_multipart","kind":"image","file_id":"` + fileID + `","upload_id":"upload"}`
	multipart := `"action":"begin","kind":"image","transport":"presigned_multipart","file_name":"a.png","mime_type":"image/png","file_size":10`
	remote := `"action":"begin","kind":"image","transport":"remote_https","remote_url":"https://example.com/a.png","correlation_id":"` + correlationID + `"`
	tests := []struct {
		name      string
		arguments string
		valid     bool
	}{
		{name: "multipart begin", arguments: `{` + multipart + `,"last_modified_ms":0}`, valid: true},
		{name: "multipart track_audio begin", arguments: `{"action":"begin","kind":"track_audio","transport":"browser_upload_page","file_name":"a.wav","mime_type":"audio/wav","file_size":10,"track_id":"` + trackID + `","expected_current_file_id":"` + current + `"}`, valid: true},
		{name: "remote begin", arguments: `{` + remote + `}`, valid: true},
		{name: "remote general begin without correlation", arguments: `{"action":"begin","kind":"general","transport":"remote_https","remote_url":"https://example.com/a.pdf"}`, valid: true},
		{name: "remote track_audio begin", arguments: `{"action":"begin","kind":"track_audio","transport":"remote_https","remote_url":"https://example.com/a.wav","track_id":"` + trackID + `","correlation_id":"` + correlationID + `"}`, valid: true},
		{name: "status", arguments: `{"action":"status","session":` + handle + `}`, valid: true},
		{name: "complete", arguments: `{"action":"complete","session":` + handle + `,"client_media_bundle_id":"` + bundleID + `"}`, valid: true},

		{name: "begin missing kind", arguments: `{"action":"begin","transport":"presigned_multipart","file_name":"a.png","mime_type":"image/png","file_size":10}`},
		{name: "begin missing transport", arguments: `{"action":"begin","kind":"image","file_name":"a.png","mime_type":"image/png","file_size":10}`},
		{name: "begin unknown transport", arguments: `{"action":"begin","kind":"image","transport":"inline","file_name":"a.png","mime_type":"image/png","file_size":10}`},
		{name: "begin null kind", arguments: `{"action":"begin","kind":null,"transport":"presigned_multipart","file_name":"a.png","mime_type":"image/png","file_size":10}`},
		{name: "begin rejects handle", arguments: `{` + multipart + `,"session":` + handle + `}`},
		{name: "begin rejects bundle", arguments: `{` + multipart + `,"client_media_bundle_id":"` + bundleID + `"}`},
		{name: "multipart missing name", arguments: `{"action":"begin","kind":"image","transport":"presigned_multipart","mime_type":"image/png","file_size":10}`},
		{name: "multipart missing mime", arguments: `{"action":"begin","kind":"image","transport":"presigned_multipart","file_name":"a.png","file_size":10}`},
		{name: "multipart missing size", arguments: `{"action":"begin","kind":"image","transport":"presigned_multipart","file_name":"a.png","mime_type":"image/png"}`},
		{name: "multipart null lm", arguments: `{` + multipart + `,"last_modified_ms":null}`},
		{name: "multipart rejects url", arguments: `{` + multipart + `,"remote_url":"https://example.com/a.png"}`},
		{name: "multipart rejects empty url", arguments: `{` + multipart + `,"remote_url":""}`},
		{name: "multipart rejects correlation", arguments: `{` + multipart + `,"correlation_id":"` + correlationID + `"}`},
		{name: "remote missing url", arguments: `{"action":"begin","kind":"image","transport":"remote_https","correlation_id":"` + correlationID + `"}`},
		{name: "remote rejects name", arguments: `{` + remote + `,"file_name":"a.png"}`},
		{name: "remote rejects empty name", arguments: `{` + remote + `,"file_name":""}`},
		{name: "remote rejects mime", arguments: `{` + remote + `,"mime_type":"image/png"}`},
		{name: "remote rejects zero size", arguments: `{` + remote + `,"file_size":0}`},
		{name: "remote rejects lm", arguments: `{` + remote + `,"last_modified_ms":0}`},
		{name: "remote non-general requires correlation", arguments: `{"action":"begin","kind":"image","transport":"remote_https","remote_url":"https://example.com/a.png"}`},
		{name: "track_audio requires track", arguments: `{"action":"begin","kind":"track_audio","transport":"remote_https","remote_url":"https://example.com/a.wav","correlation_id":"` + correlationID + `"}`},
		{name: "non-track rejects track", arguments: `{` + multipart + `,"track_id":"` + trackID + `"}`},
		{name: "non-track rejects empty track", arguments: `{` + multipart + `,"track_id":""}`},
		{name: "non-track rejects CAS", arguments: `{` + remote + `,"expected_current_file_id":"` + current + `"}`},
		{name: "status rejects kind", arguments: `{"action":"status","kind":"image","session":` + handle + `}`},
		{name: "status rejects bundle", arguments: `{"action":"status","session":` + handle + `,"client_media_bundle_id":"` + bundleID + `"}`},
		{name: "status null handle", arguments: `{"action":"status","session":null}`},
		{name: "complete rejects url", arguments: `{"action":"complete","session":` + handle + `,"remote_url":"https://example.com/a.png"}`},
		{name: "complete null bundle", arguments: `{"action":"complete","session":` + handle + `,"client_media_bundle_id":null}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			delivery := fileTestDelivery(fileID)
			runtime := &recordingMCPFileRuntime{
				initiateResult: &filemediadomain.MultipartUpload{FileID: fileID, UploadID: "upload", TotalParts: 1, ChunkSize: 1024, Status: filemediadomain.UploadSessionStatusInitiated},
				findResult:     &filemediadomain.MultipartCandidate{FileID: fileStringPointer(fileID), UploadID: fileStringPointer("upload"), TotalParts: 1, ChunkSize: 1024, Status: filemediadomain.UploadSessionStatusUploading},
				completeResult: &filemediadomain.CompletedUpload{FileID: fileID, Delivery: delivery},
				downloadResult: &filemediadomain.ImportedFile{FileID: fileID, Delivery: delivery},
			}
			tools := mustFileTools(t, runtime)
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileTransfer, fileToolArguments(t, test.arguments))
			reached := runtime.initiateRequest != nil || runtime.findRequest != nil || runtime.completeRequest != nil || runtime.downloadRequest != nil
			if test.valid {
				if err != nil || result.IsError || !reached {
					t.Fatalf("valid input rejected: %+v, %v", result, err)
				}
				return
			}
			var executionErr *mcpserver.ToolExecutionError
			if !errors.As(err, &executionErr) || reached {
				t.Fatalf("invalid input = %+v, %T %v, reached owner %v", result, err, err, reached)
			}
		})
	}
}
