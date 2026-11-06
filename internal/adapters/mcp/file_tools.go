package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/echovisionlab/geul-api/internal/adapters/filemedia"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
)

const (
	ToolFileTransfer = "file_transfer"
	ToolFileRead     = "file_read"
	ToolFileUpload   = "file_upload"
)

var fileTools = toolRegistry[FileTools]{
	fileTransferTool(mcpserver.Tool{
		Name: ToolFileTransfer, Title: "Transfer a File",
		Description: "Begin, inspect, complete or abort one File ingest through the existing File authority. " +
			"To upload a local file: begin with transport=presigned_multipart returns upload.part_uploads; PUT each part's exact bytes to its URL, for example with curl -T, then call complete with session. Nothing else is needed: the bytes are verified at completion. " +
			"URLs expire in minutes and a large File gets at most 32 at a time; parts takes session and returns URLs for the parts still missing. abort takes session and discards an unfinished session. " +
			"Field sets per action: begin multipart (transport=browser_upload_page or presigned_multipart) takes kind, transport, file_name, mime_type, file_size and optional last_modified_ms; " +
			"begin remote_https takes kind, transport, remote_url and correlation_id; status, parts and abort take session; complete takes session and optional client_media_bundle_id. Other fields are rejected. " +
			"transport=browser_upload_page leaves the bytes to the authenticated browser upload flow. Direct audio/video requires browser-prepared derivatives and gets no upload URLs; " +
			"complete must pass the client_media_bundle_id returned by status after browser preparation. " +
			"For a public HTTPS source, remote_https uses the existing server import and processing flow; all kinds except general require a stable correlation_id reused on retry. " +
			"track_audio requires track_id and attaches through native Track authority/CAS. Omit expected_current_file_id only when the Track has no original audio; otherwise copy its current File UUID. " +
			"Pass the returned session unchanged, including its Track/CAS fields.",
		InputSchema: fileTransferInputSchema(), OutputSchema: fileTransferOutputSchema(),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(false, false, true),
		Meta:            oauthSecurityMeta(),
	}),
	defineTool(mcpserver.Tool{
		Name: ToolFileRead, Title: "Read a File",
		Description: "Read authorized File metadata and existing delivery or derivative references.",
		InputSchema: fileReadInputSchema(), OutputSchema: fileReadOutputSchema(),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(true, false, false),
		Meta:            oauthSecurityMeta(),
	}, (*FileTools).read),
	defineTool(mcpserver.Tool{
		Name: ToolFileUpload, Title: "Upload a chat attachment",
		Description: "Import one uploaded chat attachment as a standalone rawsector File. Pass the connector-resolved file descriptor and a stable correlation_id reused on retry. File bytes are streamed, verified, and processed by existing File authority. MIME is sniffed from bytes; no document or Track association is created. Use file_transfer for Track audio.",
		InputSchema: json.RawMessage(fileUploadInputJSONSchema), OutputSchema: fileReadOutputSchema(),
		SecuritySchemes: oauthSecuritySchemes(), Annotations: toolAnnotations(false, false, true),
		Meta: fileUploadMeta(),
	}, (*FileTools).upload),
}

func fileUploadMeta() map[string]any {
	meta := oauthSecurityMeta()
	meta["openai/fileParams"] = []string{"file"}
	return meta
}

// FileTools exposes the File-owned MCP facade without reimplementing ingest,
// delivery, verification, or authorization in the MCP transport.
type FileTools struct {
	files *filemedia.MCPFileFacade
}

func NewFileTools(files *filemedia.MCPFileFacade) (*FileTools, error) {
	if files == nil {
		return nil, errors.New("MCP File facade is required")
	}
	return &FileTools{files: files}, nil
}

func (*FileTools) ToolNames() []string { return fileTools.names() }

func (*FileTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return fileTools.list(), nil
}

func (tools *FileTools) CallTool(
	ctx context.Context,
	_ mcpserver.Principal,
	name string,
	arguments mcpserver.ToolArguments,
) (mcpserver.ToolResult, error) {
	return fileTools.call(tools, ctx, name, arguments)
}

func (tools *FileTools) upload(ctx context.Context, _ string, call toolCall[fileUploadArguments]) (mcpserver.ToolResult, error) {
	arguments := call.Arguments

	var input fileUploadArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if err := rejectNullArguments(arguments, "file", "kind", "correlation_id"); err != nil {
		return executionError(err)
	}
	file, err := tools.files.Upload(ctx, input.File, input.Kind, input.CorrelationID)
	if err != nil {
		return fileToolCallError(err)
	}
	encoded, err := json.Marshal(verifiedFileOutput(file))
	if err != nil {
		return mcpserver.ToolResult{}, err
	}
	return structuredResult(encoded, false)
}

type fileBeginArguments struct {
	Action                string                     `json:"action"`
	Kind                  filemedia.MCPFileKind      `json:"kind"`
	Transport             filemedia.MCPFileTransport `json:"transport"`
	FileName              string                     `json:"file_name,omitempty"`
	MIMEType              string                     `json:"mime_type,omitempty"`
	FileSize              int64                      `json:"file_size,omitempty"`
	LastModified          *int64                     `json:"last_modified_ms,omitempty"`
	RemoteURL             string                     `json:"remote_url,omitempty"`
	TrackID               string                     `json:"track_id,omitempty"`
	ExpectedCurrentFileID *string                    `json:"expected_current_file_id,omitempty"`
	CorrelationID         string                     `json:"correlation_id,omitempty"`
}

// fileSessionReference is the upload session a caller passes back unchanged.
type fileSessionReference struct {
	Transport             filemedia.MCPFileTransport `json:"transport"`
	Kind                  filemedia.MCPFileKind      `json:"kind"`
	FileID                string                     `json:"file_id"`
	UploadID              string                     `json:"upload_id"`
	TrackID               string                     `json:"track_id,omitempty"`
	ExpectedCurrentFileID *string                    `json:"expected_current_file_id,omitempty"`
}

type fileSessionArguments struct {
	Action  string                `json:"action"`
	Session *fileSessionReference `json:"session"`
}

type fileCompleteArguments struct {
	fileSessionArguments
	ClientMediaBundleID string `json:"client_media_bundle_id,omitempty"`
}

type fileReadArguments struct {
	FileID string `json:"file_id"`
}

func (tools *FileTools) transfer(ctx context.Context, call fileTransferCall) (mcpserver.ToolResult, error) {
	arguments := call.Arguments

	action, err := fileTransferAction(arguments)
	if err != nil {
		return executionError(err)
	}

	var result filemedia.MCPFileTransferResult
	switch action {
	case "begin":
		var input fileBeginArguments
		if err := call.Begin.Decode(&input); err != nil {
			return executionError(err)
		}
		if err := rejectNullArguments(arguments, "kind", "transport", "file_name", "mime_type", "file_size", "last_modified_ms", "remote_url", "track_id", "expected_current_file_id", "correlation_id"); err != nil {
			return executionError(err)
		}
		if err := validateFileBeginFields(arguments, input); err != nil {
			return executionError(err)
		}
		result, err = tools.files.Begin(ctx, filemedia.MCPFileBeginInput{
			Kind: input.Kind, Transport: input.Transport,
			FileName: input.FileName, MIMEType: input.MIMEType, FileSize: input.FileSize,
			FileLastModified: input.LastModified, RemoteURL: input.RemoteURL,
			TrackID: input.TrackID, ExpectedCurrentFileID: input.ExpectedCurrentFileID, CorrelationID: input.CorrelationID,
		})
	case "status":
		var input fileSessionArguments
		if err := call.Status.Decode(&input); err != nil {
			return executionError(err)
		}
		if err := rejectNullArguments(arguments, "session"); err != nil {
			return executionError(err)
		}
		handle, handleErr := fileSessionHandle(input.Session)
		if handleErr != nil {
			return executionError(handleErr)
		}
		result, err = tools.files.Status(ctx, handle)
	case "parts", "abort":
		var input fileSessionArguments
		if err := call.Status.Decode(&input); err != nil {
			return executionError(err)
		}
		if err := rejectNullArguments(arguments, "session"); err != nil {
			return executionError(err)
		}
		handle, handleErr := fileSessionHandle(input.Session)
		if handleErr != nil {
			return executionError(handleErr)
		}
		if action == "parts" {
			result, err = tools.files.Parts(ctx, handle)
		} else {
			result, err = tools.files.Abort(ctx, handle)
		}
	case "complete":
		var input fileCompleteArguments
		if err := call.Complete.Decode(&input); err != nil {
			return executionError(err)
		}
		if err := rejectNullArguments(arguments, "session", "client_media_bundle_id"); err != nil {
			return executionError(err)
		}
		handle, handleErr := fileSessionHandle(input.Session)
		if handleErr != nil {
			return executionError(handleErr)
		}
		handle.ClientMediaBundleID = input.ClientMediaBundleID
		result, err = tools.files.Complete(ctx, handle)
	default:
		return executionError(fmt.Errorf("unsupported file transfer action %q", action))
	}
	if err != nil {
		return fileToolCallError(err)
	}
	encoded, err := encodeFileTransferResult(result)
	if err != nil {
		return mcpserver.ToolResult{}, err
	}
	return structuredResult(encoded, false)
}

func (tools *FileTools) read(ctx context.Context, _ string, call toolCall[fileReadArguments]) (mcpserver.ToolResult, error) {
	var input fileReadArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	file, err := tools.files.Read(ctx, input.FileID)
	if err != nil {
		return fileToolCallError(err)
	}
	encoded, err := json.Marshal(verifiedFileOutput(file))
	if err != nil {
		return mcpserver.ToolResult{}, fmt.Errorf("encode MCP File handle: %w", err)
	}
	return structuredResult(encoded, false)
}

func fileTransferAction(arguments mcpserver.ToolArguments) (string, error) {
	raw, ok := arguments["action"]
	if !ok {
		return "", errors.New("file transfer action is required")
	}
	var action string
	if err := json.Unmarshal(raw, &action); err != nil {
		return "", errors.New("file transfer action must be a string")
	}
	action = strings.TrimSpace(action)
	if action == "" {
		return "", errors.New("file transfer action is required")
	}
	return action, nil
}

// validateFileBeginFields enforces the per-transport and per-kind field sets
// that the flat file_transfer input schema can no longer express. Strict
// decoding already rejects fields foreign to begin (session, client_media_bundle_id);
// this check rejects present-but-inapplicable fields even when empty, before
// the facade validates values.
func validateFileBeginFields(arguments mcpserver.ToolArguments, input fileBeginArguments) error {
	for _, name := range []string{"kind", "transport"} {
		if _, ok := arguments[name]; !ok {
			return fmt.Errorf("begin requires %s", name)
		}
	}
	switch input.Transport {
	case filemedia.MCPFileTransportBrowserUploadPage, filemedia.MCPFileTransportPresignedMultipart:
		if err := requireFileArguments(arguments, "begin multipart", "file_name", "mime_type", "file_size"); err != nil {
			return err
		}
		if err := forbidFileArguments(arguments, "begin multipart", "remote_url", "correlation_id"); err != nil {
			return err
		}
	case filemedia.MCPFileTransportRemoteHTTPS:
		if err := requireFileArguments(arguments, "begin remote_https", "remote_url"); err != nil {
			return err
		}
		if err := forbidFileArguments(arguments, "begin remote_https", "file_name", "mime_type", "file_size", "last_modified_ms"); err != nil {
			return err
		}
		if input.Kind != filemedia.MCPFileKindGeneral {
			if err := requireFileArguments(arguments, "begin remote_https for kind "+string(input.Kind), "correlation_id"); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("transport must be browser_upload_page, presigned_multipart, or remote_https, got %q", input.Transport)
	}
	if input.Kind == filemedia.MCPFileKindTrackAudio {
		return requireFileArguments(arguments, "begin track_audio", "track_id")
	}
	return forbidFileArguments(arguments, "begin for kind "+string(input.Kind), "track_id", "expected_current_file_id")
}

func requireFileArguments(arguments mcpserver.ToolArguments, scope string, names ...string) error {
	for _, name := range names {
		if _, ok := arguments[name]; !ok {
			return fmt.Errorf("%s requires %s", scope, name)
		}
	}
	return nil
}

func forbidFileArguments(arguments mcpserver.ToolArguments, scope string, names ...string) error {
	for _, name := range names {
		if _, ok := arguments[name]; ok {
			return fmt.Errorf("%s must not include %s", scope, name)
		}
	}
	return nil
}

func fileSessionHandle(session *fileSessionReference) (filemedia.MCPFileSessionHandle, error) {
	if session == nil {
		return filemedia.MCPFileSessionHandle{}, errors.New("session is required: pass the session returned by begin or status")
	}
	handle := filemedia.MCPFileSessionHandle{Transport: session.Transport, Kind: session.Kind, FileID: session.FileID, UploadID: session.UploadID}
	if handle.Kind == filemedia.MCPFileKindTrackAudio {
		if session.TrackID == "" {
			return filemedia.MCPFileSessionHandle{}, errors.New("Track audio session must keep its track_id and expected_current_file_id")
		}
		handle.TrackID, handle.ExpectedCurrentFileID = session.TrackID, session.ExpectedCurrentFileID
	} else if session.TrackID != "" || session.ExpectedCurrentFileID != nil {
		return filemedia.MCPFileSessionHandle{}, errors.New("independent File session must not have a track_id or expected_current_file_id")
	}
	return handle, nil
}

type fileTransferOutput struct {
	State   filemedia.MCPFileTransferState `json:"state"`
	Session *fileSessionOutput             `json:"upload,omitempty"`
	File    *fileHandleOutput              `json:"file,omitempty"`
}

type filePartOutput struct {
	PartNumber int32  `json:"part_number"`
	URL        string `json:"url"`
	Size       int64  `json:"size"`
}

type fileSessionOutput struct {
	Session             fileSessionReference           `json:"session"`
	State               filemedia.MCPFileTransferState `json:"state"`
	FileName            string                         `json:"file_name,omitempty"`
	MIMEType            string                         `json:"mime_type,omitempty"`
	FileSize            int64                          `json:"file_size,omitempty"`
	TotalParts          int32                          `json:"total_parts"`
	ChunkSize           int32                          `json:"chunk_size"`
	UploadedParts       []int32                        `json:"uploaded_parts"`
	LastActivityAt      string                         `json:"last_activity_at,omitempty"`
	ClientMediaBundleID string                         `json:"client_media_bundle_id,omitempty"`
	PartUploads         []filePartOutput               `json:"part_uploads,omitempty"`
	PartUploadsExpireAt string                         `json:"part_uploads_expire_at,omitempty"`
}

type fileReferenceOutput struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	URL       string `json:"url"`
	Extension string `json:"extension,omitempty"`
	MIMEType  string `json:"mime_type,omitempty"`
	FileSize  int64  `json:"file_size,omitempty"`
	FileName  string `json:"file_name,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

type fileHandleOutput struct {
	ID                   string                `json:"file_id"`
	FileName             string                `json:"file_name"`
	Extension            string                `json:"extension"`
	MIMEType             string                `json:"mime_type"`
	FileSize             int64                 `json:"file_size"`
	DurationSeconds      *int32                `json:"duration_seconds,omitempty"`
	DerivativeStatus     string                `json:"derivative_status,omitempty"`
	DerivativePercentage *int32                `json:"derivative_percentage,omitempty"`
	References           []fileReferenceOutput `json:"references"`
}

func encodeFileTransferResult(result filemedia.MCPFileTransferResult) ([]byte, error) {
	compact := fileTransferOutput{State: result.State}
	switch result.State {
	case filemedia.MCPFileTransferInitiated, filemedia.MCPFileTransferUploading, filemedia.MCPFileTransferFinalizing:
		if result.Session == nil || result.File != nil || result.Session.State != result.State {
			return nil, errors.New("MCP File facade returned an invalid active transfer result")
		}
		compact.Session = fileSessionOutputFrom(*result.Session)
	case filemedia.MCPFileTransferReady:
		if result.File == nil || result.Session != nil {
			return nil, errors.New("MCP File facade returned an invalid ready transfer result")
		}
		file := verifiedFileOutput(*result.File)
		compact.File = &file
	case filemedia.MCPFileTransferAborted:
		if result.Session != nil || result.File != nil {
			return nil, errors.New("MCP File facade returned an invalid aborted transfer result")
		}
	default:
		return nil, errors.New("MCP File facade returned an unknown transfer state")
	}
	encoded, err := json.Marshal(compact)
	if err != nil {
		return nil, fmt.Errorf("encode MCP File transfer result: %w", err)
	}
	return encoded, nil
}

func fileSessionOutputFrom(session filemedia.MCPFileTransferSession) *fileSessionOutput {
	result := &fileSessionOutput{
		Session: fileSessionReference{
			Transport: session.Handle.Transport, Kind: session.Handle.Kind,
			FileID: session.Handle.FileID, UploadID: session.Handle.UploadID,
		},
		State: session.State, FileName: session.FileName, MIMEType: session.MIMEType,
		FileSize: session.FileSize, TotalParts: session.TotalParts, ChunkSize: session.ChunkSize,
		UploadedParts:       append([]int32(nil), session.UploadedPartNumbers...),
		ClientMediaBundleID: session.Handle.ClientMediaBundleID,
	}
	if session.Handle.Kind == filemedia.MCPFileKindTrackAudio {
		result.Session.TrackID = session.Handle.TrackID
		result.Session.ExpectedCurrentFileID = session.Handle.ExpectedCurrentFileID
	}
	if result.UploadedParts == nil {
		result.UploadedParts = []int32{}
	}
	if session.LastActivityAt != nil {
		result.LastActivityAt = session.LastActivityAt.UTC().Format(time.RFC3339Nano)
	}
	for _, part := range session.PartUploads {
		result.PartUploads = append(result.PartUploads, filePartOutput{PartNumber: part.PartNumber, URL: part.URL, Size: part.Size})
		result.PartUploadsExpireAt = part.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return result
}

func verifiedFileOutput(file filemedia.MCPVerifiedFileHandle) fileHandleOutput {
	references := make([]fileReferenceOutput, 0, len(file.References))
	for _, reference := range file.References {
		compact := fileReferenceOutput{
			Kind: string(reference.Kind), ID: reference.ID, URL: reference.URL, Extension: reference.Extension,
			MIMEType: reference.MIMEType, FileSize: reference.FileSize, FileName: reference.FileName,
		}
		if reference.ExpiresAt != nil {
			compact.ExpiresAt = reference.ExpiresAt.UTC().Format(time.RFC3339Nano)
		}
		references = append(references, compact)
	}
	return fileHandleOutput{
		ID: file.ID, FileName: file.FileName, Extension: file.Extension, MIMEType: file.MIMEType,
		FileSize: file.FileSize, DurationSeconds: file.DurationSeconds,
		DerivativeStatus: file.DerivativeStatus, DerivativePercentage: file.DerivativePercentage,
		References: references,
	}
}

func fileToolCallError(err error) (mcpserver.ToolResult, error) {
	if errors.Is(err, filemedia.ErrInvalidMCPFileInput) {
		return executionError(err)
	}
	switch errs.CodeOf(err) {
	case connect.CodeInvalidArgument, connect.CodeNotFound, connect.CodeAlreadyExists,
		connect.CodePermissionDenied, connect.CodeUnauthenticated,
		connect.CodeFailedPrecondition, connect.CodeAborted, connect.CodeOutOfRange:
		return mcpserver.ToolResult{}, &mcpserver.ToolExecutionError{Message: fileToolErrorText(err), Cause: err}
	default:
		return mcpserver.ToolResult{}, err
	}
}

// fileToolErrorText is err's text with a use-case error in it read as its
// Connect form ("invalid_argument: ..."), the text File tools answered when
// File use cases returned Connect errors.
func fileToolErrorText(err error) string {
	text := err.Error()
	var useCase *domainerr.Error
	if !errors.As(err, &useCase) {
		var legacy *connect.Error
		if !errors.As(err, &legacy) {
			if classified, ok := errs.Classified(err); ok {
				return classified.Error()
			}
		}
		return text
	}
	classified, ok := errs.Classified(useCase)
	if !ok {
		return text
	}
	return strings.Replace(text, useCase.Error(), classified.Error(), 1)
}

var (
	_ mcpserver.ToolRegistry   = (*FileTools)(nil)
	_ mcpserver.ToolDispatcher = (*FileTools)(nil)
	_ ToolProvider             = (*FileTools)(nil)
)

type fileUploadArguments = struct {
	File          filemedia.MCPFileUploadInput `json:"file"`
	Kind          filemedia.MCPFileKind        `json:"kind"`
	CorrelationID string                       `json:"correlation_id"`
}

type fileTransferCall struct {
	Arguments mcpserver.ToolArguments
	Begin     toolCall[fileBeginArguments]
	Status    toolCall[fileSessionArguments]
	Complete  toolCall[fileCompleteArguments]
}

func fileTransferTool(definition mcpserver.Tool) registeredTool[FileTools] {
	schema, err := generateInputSchema(definition.InputSchema,
		reflect.TypeFor[fileBeginArguments](), reflect.TypeFor[fileSessionArguments](), reflect.TypeFor[fileCompleteArguments]())
	if err != nil {
		panic(fmt.Sprintf("MCP tool %s input schema: %v", definition.Name, err))
	}
	definition.InputSchema = schema
	return registeredTool[FileTools]{definition: definition, call: func(tools *FileTools, ctx context.Context, _ string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
		return tools.transfer(ctx, fileTransferCall{
			Arguments: arguments,
			Begin:     toolCall[fileBeginArguments]{Arguments: arguments},
			Status:    toolCall[fileSessionArguments]{Arguments: arguments},
			Complete:  toolCall[fileCompleteArguments]{Arguments: arguments},
		})
	}}
}
