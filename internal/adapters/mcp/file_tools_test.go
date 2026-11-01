package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/echovisionlab/geul-api/internal/adapters/filemedia"
	"github.com/echovisionlab/geul-api/internal/auth"
	filemediadomain "github.com/echovisionlab/geul-api/internal/filemedia"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type recordingMCPFileRuntime struct {
	importInput *filemediadomain.RemoteFileImportInput
	user        *auth.UserInfo

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

func (runtime *recordingMCPFileRuntime) ImportRemoteFile(ctx context.Context, input filemediadomain.RemoteFileImportInput) (*managev1.DownloadFromUrlResponse, error) {
	runtime.capture(ctx)
	runtime.importInput = &input
	return runtime.downloadResult, runtime.downloadError
}

func (runtime *recordingMCPFileRuntime) capture(ctx context.Context) {
	runtime.user = auth.GetUser(ctx)
}

// The fake records each command as the request a Connect client would have
// sent, so the assertions read the native field names.
func (runtime *recordingMCPFileRuntime) InitiateMultipartUpload(ctx context.Context, command filemediadomain.InitiateMultipartUploadCommand) (*managev1.InitiateMultipartUploadResponse, error) {
	runtime.capture(ctx)
	runtime.initiateRequest = &managev1.InitiateMultipartUploadRequest{
		UploadType: command.UploadType, EntityId: command.EntityID, FileSize: command.FileSize,
		MimeType: command.MimeType, EntityType: command.EntityType, FileName: command.FileName,
		FileLastModified: command.FileLastModified, SlotId: command.SlotID, ExpectedCurrentFileId: command.ExpectedCurrentFileID,
	}
	if runtime.initiateError != nil {
		return nil, runtime.initiateError
	}
	return runtime.initiateResult, nil
}

func (runtime *recordingMCPFileRuntime) FindMultipartUploadCandidate(ctx context.Context, query filemediadomain.FindMultipartUploadCandidateQuery) (*managev1.FindMultipartUploadCandidateResponse, error) {
	runtime.capture(ctx)
	runtime.findRequest = &managev1.FindMultipartUploadCandidateRequest{
		UploadType: query.UploadType, EntityId: query.EntityID, FileId: query.FileID, FileName: query.FileName,
		FileSize: query.FileSize, MimeType: query.MimeType, FileLastModified: query.FileLastModified,
		EntityType: query.EntityType, SlotId: query.SlotID, UploadId: query.UploadID, ExpectedCurrentFileId: query.ExpectedCurrentFileID,
	}
	if runtime.findError != nil {
		return nil, runtime.findError
	}
	return runtime.findResult, nil
}

func (runtime *recordingMCPFileRuntime) CompleteMultipartUpload(ctx context.Context, command filemediadomain.CompleteMultipartUploadCommand) (*managev1.CompleteMultipartUploadResponse, error) {
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

func (runtime *recordingMCPFileRuntime) DownloadFromURL(ctx context.Context, command filemediadomain.DownloadFromURLCommand) (*managev1.DownloadFromUrlResponse, error) {
	runtime.capture(ctx)
	runtime.downloadRequest = &managev1.DownloadFromUrlRequest{
		UploadType: command.UploadType, EntityId: command.EntityID, Url: command.URL, EntityType: command.EntityType,
		CorrelationId: optionalRecordedString(command.CorrelationID), SlotId: optionalRecordedString(command.SlotID),
		ExpectedCurrentFileId: command.ExpectedCurrentFileID,
	}
	if runtime.downloadError != nil {
		return nil, runtime.downloadError
	}
	return runtime.downloadResult, nil
}

func (runtime *recordingMCPFileRuntime) GetMediaDelivery(ctx context.Context, query filemediadomain.GetMediaDeliveryQuery) (*managev1.GetMediaDeliveryResponse, error) {
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

func TestFileToolsTransferActionsUseOneCompactSessionHandle(t *testing.T) {
	fileID := uuid.NewString()
	bundleID := uuid.NewString()
	lastActivity := time.Date(2026, 8, 23, 10, 11, 12, 123, time.FixedZone("test", 9*60*60))
	runtime := &recordingMCPFileRuntime{
		initiateResult: &managev1.InitiateMultipartUploadResponse{
			FileId: fileID, UploadId: "upload-a", TotalParts: 3, ChunkSize: 1024,
			Status: managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_INITIATED,
		},
		findResult: &managev1.FindMultipartUploadCandidateResponse{
			ClientMediaBundleId: &bundleID,
			FileId:              fileStringPointer(fileID), UploadId: fileStringPointer("upload-a"),
			FileName: fileStringPointer("audio.wav"), MimeType: fileStringPointer("audio/wav"), FileSize: 3072,
			TotalParts: 3, ChunkSize: 1024,
			Status:         managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_UPLOADING,
			UploadedParts:  []*managev1.UploadPartInfo{{PartNumber: 1, Etag: "must-not-leak"}, {PartNumber: 3, Etag: "must-not-leak"}},
			LastActivityAt: timestamppb.New(lastActivity),
		},
	}
	tools := mustFileTools(t, runtime)

	begin, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileTransfer, fileToolArguments(t,
		`{"a":"begin","k":"audio","t":"presigned_multipart","n":"audio.wav","m":"audio/wav","s":3072,"lm":123}`))
	if err != nil {
		t.Fatalf("file_transfer(begin) error = %v", err)
	}
	if request := runtime.initiateRequest; request == nil || request.GetFileName() != "audio.wav" ||
		request.GetMimeType() != "audio/wav" || request.GetFileSize() != 3072 || request.GetFileLastModified() != 123 ||
		request.GetUploadType() != managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO {
		t.Fatalf("initiate request = %+v", request)
	}
	session := begin.StructuredContent["x"].(map[string]any)
	handle := session["h"].([]any)
	if !reflect.DeepEqual(handle, []any{"presigned_multipart", "audio", fileID, "upload-a"}) || session["s"] != "initiated" {
		t.Fatalf("begin session = %+v", session)
	}
	if strings.Contains(begin.Content[0]["text"].(string), "must-not-leak") {
		t.Fatalf("begin result leaked runtime-only value: %+v", begin)
	}

	statusArguments := `{"a":"status","h":["presigned_multipart","audio","` + fileID + `","upload-a"]}`
	status, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileTransfer, fileToolArguments(t, statusArguments))
	if err != nil {
		t.Fatalf("file_transfer(status) error = %v", err)
	}
	if request := runtime.findRequest; request == nil || request.GetFileId() != fileID || request.GetUploadId() != "upload-a" ||
		request.GetUploadType() != managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO {
		t.Fatalf("status request = %+v", request)
	}
	statusText := status.Content[0]["text"].(string)
	statusSession := status.StructuredContent["x"].(map[string]any)
	if statusSession["client_media_bundle_id"] != bundleID || !reflect.DeepEqual(statusSession["h"], handle) {
		t.Fatalf("status changed the four-slot handle or lost bundle metadata: %+v", statusSession)
	}
	if !strings.Contains(statusText, `"u":[1,3]`) || !strings.Contains(statusText, `"a":"2026-08-23T01:11:12.000000123Z"`) ||
		strings.Contains(statusText, "must-not-leak") {
		t.Fatalf("status result = %s", statusText)
	}
}

func TestFileToolsCompleteRemoteAndReadReturnVerifiedReferences(t *testing.T) {
	fileID := uuid.NewString()
	bundleID := uuid.NewString()
	delivery := fileTestDelivery(fileID)
	runtime := &recordingMCPFileRuntime{
		completeResult: &managev1.CompleteMultipartUploadResponse{FileId: fileID, Delivery: delivery},
		downloadResult: &managev1.DownloadFromUrlResponse{FileId: fileID, Delivery: delivery},
		deliveryResult: &managev1.GetMediaDeliveryResponse{Delivery: delivery},
	}
	tools := mustFileTools(t, runtime)

	completeArguments := `{"a":"complete","h":["browser_upload_page","video","` + fileID + `","upload-v"],"client_media_bundle_id":"` + bundleID + `"}`
	completed, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileTransfer, fileToolArguments(t, completeArguments))
	if err != nil {
		t.Fatalf("file_transfer(complete) error = %v", err)
	}
	if runtime.completeRequest.GetFileId() != fileID || runtime.completeRequest.GetUploadId() != "upload-v" ||
		runtime.completeRequest.GetClientMediaBundleId() != bundleID {
		t.Fatalf("complete request = %+v", runtime.completeRequest)
	}
	if completed.StructuredContent["s"] != "ready" || !strings.Contains(completed.Content[0]["text"].(string), `"r":[["inline"`) {
		t.Fatalf("complete result = %+v", completed)
	}

	remote, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileTransfer, fileToolArguments(t,
		`{"a":"begin","k":"video","t":"remote_https","u":"https://example.com/video.mp4","correlation_id":"`+uuid.NewString()+`"}`))
	if err != nil {
		t.Fatalf("file_transfer(remote begin) error = %v", err)
	}
	if runtime.downloadRequest.GetUrl() != "https://example.com/video.mp4" || remote.StructuredContent["s"] != "ready" {
		t.Fatalf("remote result/request = %+v / %+v", remote, runtime.downloadRequest)
	}

	read, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileRead, fileToolArguments(t, `{"f":"`+fileID+`"}`))
	if err != nil {
		t.Fatalf("file_read error = %v", err)
	}
	if runtime.deliveryRequest.GetFileId() != fileID || read.StructuredContent["i"] != fileID ||
		strings.Contains(read.Content[0]["text"].(string), "sha256-must-not-leak") {
		t.Fatalf("file_read result/request = %+v / %+v", read, runtime.deliveryRequest)
	}
}

func TestFileToolsTrackAudioHandleRoundTripPreservesTargetAndCAS(t *testing.T) {
	for _, current := range []string{"", uuid.NewString()} {
		t.Run(current, func(t *testing.T) {
			fileID, trackID, bundleID := uuid.NewString(), uuid.NewString(), uuid.NewString()
			runtime := &recordingMCPFileRuntime{
				initiateResult: &managev1.InitiateMultipartUploadResponse{FileId: fileID, UploadId: "track-upload", TotalParts: 1, ChunkSize: 1024, Status: managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_INITIATED},
				findResult:     &managev1.FindMultipartUploadCandidateResponse{FileId: fileStringPointer(fileID), UploadId: fileStringPointer("track-upload"), TotalParts: 1, ChunkSize: 1024, Status: managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_UPLOADING, ClientMediaBundleId: &bundleID},
				completeResult: &managev1.CompleteMultipartUploadResponse{FileId: fileID, Delivery: fileTestDelivery(fileID)},
			}
			tools := mustFileTools(t, runtime)
			arguments := `{"a":"begin","k":"track_audio","t":"browser_upload_page","track_id":"` + trackID + `","n":"track.wav","m":"audio/wav","s":1024`
			if current != "" {
				arguments += `,"expected_current_file_id":"` + current + `"`
			}
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileTransfer, fileToolArguments(t, arguments+`}`))
			if err != nil {
				t.Fatal(err)
			}
			handle := result.StructuredContent["x"].(map[string]any)["h"]
			want := []any{"browser_upload_page", "track_audio", fileID, "track-upload", trackID, current}
			if !reflect.DeepEqual(handle, want) || runtime.initiateRequest.GetEntityId() != trackID || runtime.initiateRequest.GetExpectedCurrentFileId() != current {
				t.Fatalf("begin target/CAS = %+v, %+v", handle, runtime.initiateRequest)
			}
			encodedHandle, _ := json.Marshal(handle)
			status, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileTransfer, fileToolArguments(t, `{"a":"status","h":`+string(encodedHandle)+`}`))
			if err != nil {
				t.Fatal(err)
			}
			statusSession := status.StructuredContent["x"].(map[string]any)
			if !reflect.DeepEqual(statusSession["h"], want) || runtime.findRequest.GetEntityId() != trackID || runtime.findRequest.GetExpectedCurrentFileId() != current || (runtime.findRequest.ExpectedCurrentFileId == nil) != (current == "") {
				t.Fatalf("status lost target/CAS = %+v, %+v", statusSession, runtime.findRequest)
			}
			_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileTransfer, fileToolArguments(t, `{"a":"complete","h":`+string(encodedHandle)+`,"client_media_bundle_id":"`+bundleID+`"}`))
			if err != nil || runtime.completeRequest.GetFileId() != fileID || runtime.completeRequest.GetClientMediaBundleId() != bundleID {
				t.Fatalf("completion bypassed native session: %+v, %v", runtime.completeRequest, err)
			}
		})
	}
}

func TestFileToolsRemoteTrackAudioForwardsStableRetryIdentity(t *testing.T) {
	fileID, trackID, current, correlationID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	runtime := &recordingMCPFileRuntime{downloadResult: &managev1.DownloadFromUrlResponse{FileId: fileID, Delivery: fileTestDelivery(fileID)}}
	tools := mustFileTools(t, runtime)
	arguments := fileToolArguments(t, `{"a":"begin","k":"track_audio","t":"remote_https","track_id":"`+trackID+`","expected_current_file_id":"`+current+`","correlation_id":"`+correlationID+`","u":"https://example.com/track.wav"}`)
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
		`{"a":"begin","k":"track_audio","t":"presigned_multipart","n":"track.wav","m":"audio/wav","s":1024}`,
		`{"a":"begin","k":"audio","t":"presigned_multipart","track_id":"` + trackID + `","n":"track.wav","m":"audio/wav","s":1024}`,
		`{"a":"begin","k":"track_audio","t":"presigned_multipart","track_id":"` + trackID + `","expected_current_file_id":"","n":"track.wav","m":"audio/wav","s":1024}`,
		`{"a":"begin","k":"track_audio","t":"presigned_multipart","track_id":"` + trackID + `","expected_current_file_id":null,"n":"track.wav","m":"audio/wav","s":1024}`,
		`{"a":"begin","k":"track_audio","t":"remote_https","track_id":"` + trackID + `","u":"https://example.com/track.wav"}`,
		`{"a":"begin","k":"audio","t":"remote_https","correlation_id":"bad","u":"https://example.com/track.wav"}`,
		`{"a":"status","h":["presigned_multipart","track_audio","` + fileID + `","upload"]}`,
		`{"a":"status","h":["presigned_multipart","audio","` + fileID + `","upload","` + trackID + `",""]}`,
		`{"a":"status","h":["presigned_multipart","track_audio","` + fileID + `","upload","` + trackID + `","bad"]}`,
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
		{name: "unknown action", tool: ToolFileTransfer, arguments: `{"a":"upload"}`},
		{name: "inline field", tool: ToolFileTransfer, arguments: `{"a":"begin","k":"image","t":"remote_https","u":"https://example.com/a.png","base64":"AA=="}`},
		{name: "data URL", tool: ToolFileTransfer, arguments: `{"a":"begin","k":"image","t":"remote_https","u":"data:image/png,AA"}`},
		{name: "remote metadata", tool: ToolFileTransfer, arguments: `{"a":"begin","k":"image","t":"remote_https","u":"https://example.com/a.png","n":"a.png"}`},
		{name: "short handle", tool: ToolFileTransfer, arguments: `{"a":"status","h":["presigned_multipart","image"]}`},
		{name: "status extra field", tool: ToolFileTransfer, arguments: `{"a":"status","h":["presigned_multipart","image","` + fileID + `","upload"],"f":"` + fileID + `"}`},
		{name: "status rejects completion bundle", tool: ToolFileTransfer, arguments: `{"a":"status","h":["presigned_multipart","image","` + fileID + `","upload"],"client_media_bundle_id":"` + uuid.NewString() + `"}`},
		{name: "read inline field", tool: ToolFileRead, arguments: `{"f":"` + fileID + `","bytes":"AA=="}`},
		{name: "read invalid ID", tool: ToolFileRead, arguments: `{"f":"not-a-uuid"}`},
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
	arguments := fileToolArguments(t, `{"f":"`+fileID+`"}`)

	runtime.deliveryError = connect.NewError(connect.CodePermissionDenied, errors.New("denied"))
	_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileRead, arguments)
	var executionErr *mcpserver.ToolExecutionError
	if !errors.As(err, &executionErr) {
		t.Fatalf("permission error = %T %v", err, err)
	}

	runtime.deliveryError = nil
	runtime.deliveryResult = &managev1.GetMediaDeliveryResponse{}
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
		deliveryResult: &managev1.GetMediaDeliveryResponse{Delivery: fileTestDelivery(fileID)},
	}
	tools := mustFileTools(t, runtime)
	dispatcher, err := WrapDispatcher(tools)
	if err != nil {
		t.Fatalf("WrapDispatcher() error = %v", err)
	}
	principal := validMCPPrincipal()
	if _, err := dispatcher.CallTool(t.Context(), principal, ToolFileRead, fileToolArguments(t, `{"f":"`+fileID+`"}`)); err != nil {
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

func fileTestDelivery(fileID string) *commonv1.MediaDelivery {
	fileName := "video.mp4"
	expiresAt := timestamppb.New(time.Date(2026, 8, 23, 11, 12, 13, 0, time.UTC))
	return &commonv1.MediaDelivery{
		FileId: fileID, FileName: &fileName, Extension: "mp4", MimeType: "video/mp4", FileSize: 42,
		Inline: &commonv1.ExpiringMediaRef{
			FileId: fileID, Url: "https://files.example.com/inline", ExpiresAt: expiresAt,
			Extension: "mp4", MimeType: "video/mp4", FileName: &fileName,
		},
		Asset: &commonv1.AssetRef{
			AssetId: "asset-a", Url: "https://cdn.example.com/video.mp4", Extension: "mp4",
			MimeType: "video/mp4", FileSize: 42, Sha256: []byte("sha256-must-not-leak"), DownloadFilename: &fileName,
		},
		ProcessingStatus: commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_READY,
	}
}

func fileStringPointer(value string) *string { return &value }

func TestFileToolsUploadAttachmentDescriptorAndStrictMapping(t *testing.T) {
	fileID, correlationID := uuid.NewString(), uuid.NewString()
	runtime := &recordingMCPFileRuntime{downloadResult: &managev1.DownloadFromUrlResponse{FileId: fileID, Delivery: fileTestDelivery(fileID)}}
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
	want := filemediadomain.RemoteFileImportInput{UploadType: managev1.UploadType_UPLOAD_TYPE_EDITOR_VIDEO, SourceURL: "https://attachments.example.com/download?signature=private", FileName: "Original.mp4", CorrelationID: correlationID}
	if runtime.importInput == nil || !reflect.DeepEqual(*runtime.importInput, want) || runtime.downloadRequest != nil {
		t.Fatalf("native input = %+v", runtime.importInput)
	}
	if result.StructuredContent["i"] != fileID || result.StructuredContent["m"] != "video/mp4" || strings.Contains(result.Content[0]["text"].(string), "private") || strings.Contains(result.Content[0]["text"].(string), "opaque") {
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
	if string(schema["type"]) != `"object"` || string(schema["additionalProperties"]) != "false" || string(schema["required"]) != `["a"]` {
		t.Fatalf("file_transfer input schema root = type %s, additionalProperties %s, required %s", schema["type"], schema["additionalProperties"], schema["required"])
	}
	var properties map[string]struct {
		Enum        []string `json:"enum"`
		Description string   `json:"description"`
	}
	if err := json.Unmarshal(schema["properties"], &properties); err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "k", "t", "n", "m", "s", "lm", "u", "track_id", "expected_current_file_id", "correlation_id", "h", "client_media_bundle_id"}
	if len(properties) != len(want) {
		t.Fatalf("file_transfer input properties = %d, want %d", len(properties), len(want))
	}
	for _, name := range want {
		property, ok := properties[name]
		if !ok || property.Description == "" {
			t.Fatalf("file_transfer input property %q missing or undocumented", name)
		}
	}
	if !reflect.DeepEqual(properties["a"].Enum, []string{"begin", "status", "complete"}) ||
		!reflect.DeepEqual(properties["t"].Enum, []string{"browser_upload_page", "presigned_multipart", "remote_https"}) {
		t.Fatalf("file_transfer action/transport enums = %v / %v", properties["a"].Enum, properties["t"].Enum)
	}
}

func TestFileToolsTransferEnforcesFlattenedSchemaRules(t *testing.T) {
	fileID, trackID, current, correlationID, bundleID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	handle := `["presigned_multipart","image","` + fileID + `","upload"]`
	multipart := `"a":"begin","k":"image","t":"presigned_multipart","n":"a.png","m":"image/png","s":10`
	remote := `"a":"begin","k":"image","t":"remote_https","u":"https://example.com/a.png","correlation_id":"` + correlationID + `"`
	tests := []struct {
		name      string
		arguments string
		valid     bool
	}{
		{name: "multipart begin", arguments: `{` + multipart + `,"lm":0}`, valid: true},
		{name: "multipart track_audio begin", arguments: `{"a":"begin","k":"track_audio","t":"browser_upload_page","n":"a.wav","m":"audio/wav","s":10,"track_id":"` + trackID + `","expected_current_file_id":"` + current + `"}`, valid: true},
		{name: "remote begin", arguments: `{` + remote + `}`, valid: true},
		{name: "remote general begin without correlation", arguments: `{"a":"begin","k":"general","t":"remote_https","u":"https://example.com/a.pdf"}`, valid: true},
		{name: "remote track_audio begin", arguments: `{"a":"begin","k":"track_audio","t":"remote_https","u":"https://example.com/a.wav","track_id":"` + trackID + `","correlation_id":"` + correlationID + `"}`, valid: true},
		{name: "status", arguments: `{"a":"status","h":` + handle + `}`, valid: true},
		{name: "complete", arguments: `{"a":"complete","h":` + handle + `,"client_media_bundle_id":"` + bundleID + `"}`, valid: true},

		{name: "begin missing kind", arguments: `{"a":"begin","t":"presigned_multipart","n":"a.png","m":"image/png","s":10}`},
		{name: "begin missing transport", arguments: `{"a":"begin","k":"image","n":"a.png","m":"image/png","s":10}`},
		{name: "begin unknown transport", arguments: `{"a":"begin","k":"image","t":"inline","n":"a.png","m":"image/png","s":10}`},
		{name: "begin null kind", arguments: `{"a":"begin","k":null,"t":"presigned_multipart","n":"a.png","m":"image/png","s":10}`},
		{name: "begin rejects handle", arguments: `{` + multipart + `,"h":` + handle + `}`},
		{name: "begin rejects bundle", arguments: `{` + multipart + `,"client_media_bundle_id":"` + bundleID + `"}`},
		{name: "multipart missing name", arguments: `{"a":"begin","k":"image","t":"presigned_multipart","m":"image/png","s":10}`},
		{name: "multipart missing mime", arguments: `{"a":"begin","k":"image","t":"presigned_multipart","n":"a.png","s":10}`},
		{name: "multipart missing size", arguments: `{"a":"begin","k":"image","t":"presigned_multipart","n":"a.png","m":"image/png"}`},
		{name: "multipart null lm", arguments: `{` + multipart + `,"lm":null}`},
		{name: "multipart rejects url", arguments: `{` + multipart + `,"u":"https://example.com/a.png"}`},
		{name: "multipart rejects empty url", arguments: `{` + multipart + `,"u":""}`},
		{name: "multipart rejects correlation", arguments: `{` + multipart + `,"correlation_id":"` + correlationID + `"}`},
		{name: "remote missing url", arguments: `{"a":"begin","k":"image","t":"remote_https","correlation_id":"` + correlationID + `"}`},
		{name: "remote rejects name", arguments: `{` + remote + `,"n":"a.png"}`},
		{name: "remote rejects empty name", arguments: `{` + remote + `,"n":""}`},
		{name: "remote rejects mime", arguments: `{` + remote + `,"m":"image/png"}`},
		{name: "remote rejects zero size", arguments: `{` + remote + `,"s":0}`},
		{name: "remote rejects lm", arguments: `{` + remote + `,"lm":0}`},
		{name: "remote non-general requires correlation", arguments: `{"a":"begin","k":"image","t":"remote_https","u":"https://example.com/a.png"}`},
		{name: "track_audio requires track", arguments: `{"a":"begin","k":"track_audio","t":"remote_https","u":"https://example.com/a.wav","correlation_id":"` + correlationID + `"}`},
		{name: "non-track rejects track", arguments: `{` + multipart + `,"track_id":"` + trackID + `"}`},
		{name: "non-track rejects empty track", arguments: `{` + multipart + `,"track_id":""}`},
		{name: "non-track rejects CAS", arguments: `{` + remote + `,"expected_current_file_id":"` + current + `"}`},
		{name: "status rejects kind", arguments: `{"a":"status","k":"image","h":` + handle + `}`},
		{name: "status rejects bundle", arguments: `{"a":"status","h":` + handle + `,"client_media_bundle_id":"` + bundleID + `"}`},
		{name: "status null handle", arguments: `{"a":"status","h":null}`},
		{name: "complete rejects url", arguments: `{"a":"complete","h":` + handle + `,"u":"https://example.com/a.png"}`},
		{name: "complete null bundle", arguments: `{"a":"complete","h":` + handle + `,"client_media_bundle_id":null}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			delivery := fileTestDelivery(fileID)
			runtime := &recordingMCPFileRuntime{
				initiateResult: &managev1.InitiateMultipartUploadResponse{FileId: fileID, UploadId: "upload", TotalParts: 1, ChunkSize: 1024, Status: managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_INITIATED},
				findResult:     &managev1.FindMultipartUploadCandidateResponse{FileId: fileStringPointer(fileID), UploadId: fileStringPointer("upload"), TotalParts: 1, ChunkSize: 1024, Status: managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_UPLOADING},
				completeResult: &managev1.CompleteMultipartUploadResponse{FileId: fileID, Delivery: delivery},
				downloadResult: &managev1.DownloadFromUrlResponse{FileId: fileID, Delivery: delivery},
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
