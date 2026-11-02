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
		Description: "Begin, inspect, or complete one File ingest through the existing File authority. " +
			"Field sets per action: begin multipart (t=browser_upload_page|presigned_multipart) takes k,t,n,m,s and optional lm; " +
			"begin remote_https takes k,t,u and correlation_id; status takes h; complete takes h and optional client_media_bundle_id. Other fields are rejected. " +
			"Multipart bytes use the existing authenticated browser upload flow. Direct audio/video requires browser-prepared derivatives; " +
			"complete must pass the client_media_bundle_id returned by status after browser preparation. " +
			"For a public HTTPS source, remote_https uses the existing server import and processing flow; all kinds except general require a stable correlation_id reused on retry. " +
			"track_audio requires track_id and attaches through native Track authority/CAS. Omit expected_current_file_id only when the Track has no original audio; otherwise copy its current File UUID. " +
			"Pass the returned session handle unchanged, including its Track/CAS fields.",
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
	encoded, err := json.Marshal(compactVerifiedFile(file))
	if err != nil {
		return mcpserver.ToolResult{}, err
	}
	return structuredResult(encoded, false)
}

type fileBeginArguments struct {
	Action                string                     `json:"a"`
	Kind                  filemedia.MCPFileKind      `json:"k"`
	Transport             filemedia.MCPFileTransport `json:"t"`
	FileName              string                     `json:"n,omitempty"`
	MIMEType              string                     `json:"m,omitempty"`
	FileSize              int64                      `json:"s,omitempty"`
	LastModified          *int64                     `json:"lm,omitempty"`
	RemoteURL             string                     `json:"u,omitempty"`
	TrackID               string                     `json:"track_id,omitempty"`
	ExpectedCurrentFileID *string                    `json:"expected_current_file_id,omitempty"`
	CorrelationID         string                     `json:"correlation_id,omitempty"`
}

type fileSessionArguments struct {
	Action string   `json:"a"`
	Handle []string `json:"h"`
}

type fileCompleteArguments struct {
	fileSessionArguments
	ClientMediaBundleID string `json:"client_media_bundle_id,omitempty"`
}

type fileReadArguments struct {
	FileID string `json:"f"`
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
		if err := rejectNullArguments(arguments, "k", "t", "n", "m", "s", "lm", "u", "track_id", "expected_current_file_id", "correlation_id"); err != nil {
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
		if err := rejectNullArguments(arguments, "h"); err != nil {
			return executionError(err)
		}
		handle, handleErr := fileSessionHandle(input.Handle)
		if handleErr != nil {
			return executionError(handleErr)
		}
		result, err = tools.files.Status(ctx, handle)
	case "complete":
		var input fileCompleteArguments
		if err := call.Complete.Decode(&input); err != nil {
			return executionError(err)
		}
		if err := rejectNullArguments(arguments, "h", "client_media_bundle_id"); err != nil {
			return executionError(err)
		}
		handle, handleErr := fileSessionHandle(input.Handle)
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
	encoded, err := json.Marshal(compactVerifiedFile(file))
	if err != nil {
		return mcpserver.ToolResult{}, fmt.Errorf("encode MCP File handle: %w", err)
	}
	return structuredResult(encoded, false)
}

func fileTransferAction(arguments mcpserver.ToolArguments) (string, error) {
	raw, ok := arguments["a"]
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
// decoding already rejects fields foreign to begin (h, client_media_bundle_id);
// this check rejects present-but-inapplicable fields even when empty, before
// the facade validates values.
func validateFileBeginFields(arguments mcpserver.ToolArguments, input fileBeginArguments) error {
	for _, name := range []string{"k", "t"} {
		if _, ok := arguments[name]; !ok {
			return fmt.Errorf("begin requires %s", name)
		}
	}
	switch input.Transport {
	case filemedia.MCPFileTransportBrowserUploadPage, filemedia.MCPFileTransportPresignedMultipart:
		if err := requireFileArguments(arguments, "begin multipart", "n", "m", "s"); err != nil {
			return err
		}
		if err := forbidFileArguments(arguments, "begin multipart", "u", "correlation_id"); err != nil {
			return err
		}
	case filemedia.MCPFileTransportRemoteHTTPS:
		if err := requireFileArguments(arguments, "begin remote_https", "u"); err != nil {
			return err
		}
		if err := forbidFileArguments(arguments, "begin remote_https", "n", "m", "s", "lm"); err != nil {
			return err
		}
		if input.Kind != filemedia.MCPFileKindGeneral {
			if err := requireFileArguments(arguments, "begin remote_https for kind "+string(input.Kind), "correlation_id"); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("t must be browser_upload_page, presigned_multipart, or remote_https, got %q", input.Transport)
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

func fileSessionHandle(values []string) (filemedia.MCPFileSessionHandle, error) {
	if len(values) != 4 && len(values) != 6 {
		return filemedia.MCPFileSessionHandle{}, errors.New("file session handle must contain four values, or six for Track audio")
	}
	handle := filemedia.MCPFileSessionHandle{
		Transport: filemedia.MCPFileTransport(values[0]),
		Kind:      filemedia.MCPFileKind(values[1]),
		FileID:    values[2],
		UploadID:  values[3],
	}
	if handle.Kind == filemedia.MCPFileKindTrackAudio {
		if len(values) != 6 {
			return filemedia.MCPFileSessionHandle{}, errors.New("Track audio session handle must preserve Track ID and current audio CAS")
		}
		handle.TrackID = values[4]
		if values[5] != "" {
			handle.ExpectedCurrentFileID = &values[5]
		}
	} else if len(values) != 4 {
		return filemedia.MCPFileSessionHandle{}, errors.New("independent File session handle must contain four values")
	}
	return handle, nil
}

type compactFileTransferResult struct {
	State   filemedia.MCPFileTransferState `json:"s"`
	Session *compactFileSession            `json:"x,omitempty"`
	File    *compactFileHandle             `json:"f,omitempty"`
}

type compactFileSession struct {
	Handle              []string                       `json:"h"`
	State               filemedia.MCPFileTransferState `json:"s"`
	FileName            string                         `json:"n,omitempty"`
	MIMEType            string                         `json:"m,omitempty"`
	FileSize            int64                          `json:"z,omitempty"`
	TotalParts          int32                          `json:"p"`
	ChunkSize           int32                          `json:"c"`
	UploadedParts       []int32                        `json:"u"`
	LastActivityAt      string                         `json:"a,omitempty"`
	ClientMediaBundleID string                         `json:"client_media_bundle_id,omitempty"`
}

type compactFileHandle struct {
	ID                   string  `json:"i"`
	FileName             string  `json:"n"`
	Extension            string  `json:"x"`
	MIMEType             string  `json:"m"`
	FileSize             int64   `json:"z"`
	DurationSeconds      *int32  `json:"d,omitempty"`
	DerivativeStatus     string  `json:"s,omitempty"`
	DerivativePercentage *int32  `json:"p,omitempty"`
	References           [][]any `json:"r"`
}

func encodeFileTransferResult(result filemedia.MCPFileTransferResult) ([]byte, error) {
	compact := compactFileTransferResult{State: result.State}
	switch result.State {
	case filemedia.MCPFileTransferInitiated, filemedia.MCPFileTransferUploading, filemedia.MCPFileTransferFinalizing:
		if result.Session == nil || result.File != nil || result.Session.State != result.State {
			return nil, errors.New("MCP File facade returned an invalid active transfer result")
		}
		compact.Session = compactFileSessionFrom(*result.Session)
	case filemedia.MCPFileTransferReady:
		if result.File == nil || result.Session != nil {
			return nil, errors.New("MCP File facade returned an invalid ready transfer result")
		}
		file := compactVerifiedFile(*result.File)
		compact.File = &file
	default:
		return nil, errors.New("MCP File facade returned an unknown transfer state")
	}
	encoded, err := json.Marshal(compact)
	if err != nil {
		return nil, fmt.Errorf("encode MCP File transfer result: %w", err)
	}
	return encoded, nil
}

func compactFileSessionFrom(session filemedia.MCPFileTransferSession) *compactFileSession {
	result := &compactFileSession{
		Handle: []string{
			string(session.Handle.Transport), string(session.Handle.Kind),
			session.Handle.FileID, session.Handle.UploadID,
		},
		State: session.State, FileName: session.FileName, MIMEType: session.MIMEType,
		FileSize: session.FileSize, TotalParts: session.TotalParts, ChunkSize: session.ChunkSize,
		UploadedParts:       append([]int32(nil), session.UploadedPartNumbers...),
		ClientMediaBundleID: session.Handle.ClientMediaBundleID,
	}
	if session.Handle.Kind == filemedia.MCPFileKindTrackAudio {
		expectedFileID := ""
		if session.Handle.ExpectedCurrentFileID != nil {
			expectedFileID = *session.Handle.ExpectedCurrentFileID
		}
		result.Handle = append(result.Handle, session.Handle.TrackID, expectedFileID)
	}
	if result.UploadedParts == nil {
		result.UploadedParts = []int32{}
	}
	if session.LastActivityAt != nil {
		result.LastActivityAt = session.LastActivityAt.UTC().Format(time.RFC3339Nano)
	}
	return result
}

func compactVerifiedFile(file filemedia.MCPVerifiedFileHandle) compactFileHandle {
	references := make([][]any, 0, len(file.References))
	for _, reference := range file.References {
		var expiresAt any
		if reference.ExpiresAt != nil {
			expiresAt = reference.ExpiresAt.UTC().Format(time.RFC3339Nano)
		}
		references = append(references, []any{
			string(reference.Kind), reference.ID, reference.URL, reference.Extension,
			reference.MIMEType, reference.FileSize, reference.FileName, expiresAt,
		})
	}
	return compactFileHandle{
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
