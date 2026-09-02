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
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type recordingMCPFileRuntime struct {
	user *auth.UserInfo

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

func (runtime *recordingMCPFileRuntime) capture(ctx context.Context) {
	runtime.user = auth.GetUser(ctx)
}

func (runtime *recordingMCPFileRuntime) InitiateMultipartUpload(
	ctx context.Context,
	request *connect.Request[managev1.InitiateMultipartUploadRequest],
) (*connect.Response[managev1.InitiateMultipartUploadResponse], error) {
	runtime.capture(ctx)
	runtime.initiateRequest = request.Msg
	if runtime.initiateError != nil {
		return nil, runtime.initiateError
	}
	return connect.NewResponse(runtime.initiateResult), nil
}

func (runtime *recordingMCPFileRuntime) FindMultipartUploadCandidate(
	ctx context.Context,
	request *connect.Request[managev1.FindMultipartUploadCandidateRequest],
) (*connect.Response[managev1.FindMultipartUploadCandidateResponse], error) {
	runtime.capture(ctx)
	runtime.findRequest = request.Msg
	if runtime.findError != nil {
		return nil, runtime.findError
	}
	return connect.NewResponse(runtime.findResult), nil
}

func (runtime *recordingMCPFileRuntime) CompleteMultipartUpload(
	ctx context.Context,
	request *connect.Request[managev1.CompleteMultipartUploadRequest],
) (*connect.Response[managev1.CompleteMultipartUploadResponse], error) {
	runtime.capture(ctx)
	runtime.completeRequest = request.Msg
	if runtime.completeError != nil {
		return nil, runtime.completeError
	}
	return connect.NewResponse(runtime.completeResult), nil
}

func (runtime *recordingMCPFileRuntime) DownloadFromUrl(
	ctx context.Context,
	request *connect.Request[managev1.DownloadFromUrlRequest],
) (*connect.Response[managev1.DownloadFromUrlResponse], error) {
	runtime.capture(ctx)
	runtime.downloadRequest = request.Msg
	if runtime.downloadError != nil {
		return nil, runtime.downloadError
	}
	return connect.NewResponse(runtime.downloadResult), nil
}

func (runtime *recordingMCPFileRuntime) GetMediaDelivery(
	ctx context.Context,
	request *connect.Request[managev1.GetMediaDeliveryRequest],
) (*connect.Response[managev1.GetMediaDeliveryResponse], error) {
	runtime.capture(ctx)
	runtime.deliveryRequest = request.Msg
	if runtime.deliveryError != nil {
		return nil, runtime.deliveryError
	}
	return connect.NewResponse(runtime.deliveryResult), nil
}

func TestFileToolsExposeCompactReferenceOnlySurface(t *testing.T) {
	tools := mustFileTools(t, &recordingMCPFileRuntime{})
	listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if got, want := tools.ToolNames(), []string{ToolFileTransfer, ToolFileRead}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ToolNames() = %v, want %v", got, want)
	}
	if len(listed) != 2 {
		t.Fatalf("ListTools() returned %d tools", len(listed))
	}
	wantAnnotations := map[string]map[string]any{
		ToolFileTransfer: toolAnnotations(false, false, true),
		ToolFileRead:     toolAnnotations(true, false, false),
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
			for _, forbidden := range []string{"base64", "bytes", "blob", "data:"} {
				if strings.Contains(lower, forbidden) {
					t.Fatalf("%s %s schema exposed %q", tool.Name, schemaName, forbidden)
				}
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
	lastActivity := time.Date(2026, 8, 23, 10, 11, 12, 123, time.FixedZone("test", 9*60*60))
	runtime := &recordingMCPFileRuntime{
		initiateResult: &managev1.InitiateMultipartUploadResponse{
			FileId: fileID, UploadId: "upload-a", TotalParts: 3, ChunkSize: 1024,
			Status: managev1.UploadSessionStatus_UPLOAD_SESSION_STATUS_INITIATED,
		},
		findResult: &managev1.FindMultipartUploadCandidateResponse{
			FileId: fileStringPointer(fileID), UploadId: fileStringPointer("upload-a"),
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
	if !strings.Contains(statusText, `"u":[1,3]`) || !strings.Contains(statusText, `"a":"2026-08-23T01:11:12.000000123Z"`) ||
		strings.Contains(statusText, "must-not-leak") {
		t.Fatalf("status result = %s", statusText)
	}
}

func TestFileToolsCompleteRemoteAndReadReturnVerifiedReferences(t *testing.T) {
	fileID := uuid.NewString()
	delivery := fileTestDelivery(fileID)
	runtime := &recordingMCPFileRuntime{
		completeResult: &managev1.CompleteMultipartUploadResponse{FileId: fileID, Delivery: delivery},
		downloadResult: &managev1.DownloadFromUrlResponse{FileId: fileID, Delivery: delivery},
		deliveryResult: &managev1.GetMediaDeliveryResponse{Delivery: delivery},
	}
	tools := mustFileTools(t, runtime)

	completeArguments := `{"a":"complete","h":["browser_upload_page","video","` + fileID + `","upload-v"]}`
	completed, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileTransfer, fileToolArguments(t, completeArguments))
	if err != nil {
		t.Fatalf("file_transfer(complete) error = %v", err)
	}
	if runtime.completeRequest.GetFileId() != fileID || runtime.completeRequest.GetUploadId() != "upload-v" {
		t.Fatalf("complete request = %+v", runtime.completeRequest)
	}
	if completed.StructuredContent["s"] != "ready" || !strings.Contains(completed.Content[0]["text"].(string), `"r":[["inline"`) {
		t.Fatalf("complete result = %+v", completed)
	}

	remote, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileTransfer, fileToolArguments(t,
		`{"a":"begin","k":"video","t":"remote_https","u":"https://example.com/video.mp4"}`))
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
