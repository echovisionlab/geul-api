package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/echovisionlab/geul-api/internal/adapters/filemedia"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
)

const (
	ToolFileTransfer = "file_transfer"
	ToolFileRead     = "file_read"
)

var fileTools = []mcpserver.Tool{
	{
		Name: ToolFileTransfer, Title: "Transfer a File",
		Description: "Begin, inspect, or complete one File ingest through the existing File authority.",
		InputSchema: fileTransferInputSchema(), OutputSchema: fileTransferOutputSchema(),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(false, false, true),
		Meta:            oauthSecurityMeta(),
	},
	{
		Name: ToolFileRead, Title: "Read a File",
		Description: "Read authorized File metadata and existing delivery or derivative references.",
		InputSchema: fileReadInputSchema(), OutputSchema: fileReadOutputSchema(),
		SecuritySchemes: oauthSecuritySchemes(),
		Annotations:     toolAnnotations(true, false, false),
		Meta:            oauthSecurityMeta(),
	},
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

func (*FileTools) ToolNames() []string {
	return toolDefinitionNames(fileTools)
}

func (*FileTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return cloneToolDefinitions(fileTools), nil
}

func (tools *FileTools) CallTool(
	ctx context.Context,
	_ mcpserver.Principal,
	name string,
	arguments mcpserver.ToolArguments,
) (mcpserver.ToolResult, error) {
	switch name {
	case ToolFileTransfer:
		return tools.transfer(ctx, arguments)
	case ToolFileRead:
		return tools.read(ctx, arguments)
	default:
		return mcpserver.ToolResult{}, mcpserver.ErrUnknownTool
	}
}

type fileBeginArguments struct {
	Action       string                     `json:"a"`
	Kind         filemedia.MCPFileKind      `json:"k"`
	Transport    filemedia.MCPFileTransport `json:"t"`
	FileName     string                     `json:"n,omitempty"`
	MIMEType     string                     `json:"m,omitempty"`
	FileSize     int64                      `json:"s,omitempty"`
	LastModified *int64                     `json:"lm,omitempty"`
	RemoteURL    string                     `json:"u,omitempty"`
}

type fileSessionArguments struct {
	Action string   `json:"a"`
	Handle []string `json:"h"`
}

type fileReadArguments struct {
	FileID string `json:"f"`
}

func (tools *FileTools) transfer(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	action, err := fileTransferAction(arguments)
	if err != nil {
		return executionError(err)
	}

	var result filemedia.MCPFileTransferResult
	switch action {
	case "begin":
		var input fileBeginArguments
		if err := decodeArguments(arguments, &input); err != nil {
			return executionError(err)
		}
		result, err = tools.files.Begin(ctx, filemedia.MCPFileBeginInput{
			Kind: input.Kind, Transport: input.Transport,
			FileName: input.FileName, MIMEType: input.MIMEType, FileSize: input.FileSize,
			FileLastModified: input.LastModified, RemoteURL: input.RemoteURL,
		})
	case "status", "complete":
		var input fileSessionArguments
		if err := decodeArguments(arguments, &input); err != nil {
			return executionError(err)
		}
		handle, handleErr := fileSessionHandle(input.Handle)
		if handleErr != nil {
			return executionError(handleErr)
		}
		if action == "status" {
			result, err = tools.files.Status(ctx, handle)
		} else {
			result, err = tools.files.Complete(ctx, handle)
		}
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

func (tools *FileTools) read(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input fileReadArguments
	if err := decodeArguments(arguments, &input); err != nil {
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

func fileSessionHandle(values []string) (filemedia.MCPFileSessionHandle, error) {
	if len(values) != 4 {
		return filemedia.MCPFileSessionHandle{}, errors.New("file session handle must contain transport, kind, File ID, and upload ID")
	}
	return filemedia.MCPFileSessionHandle{
		Transport: filemedia.MCPFileTransport(values[0]),
		Kind:      filemedia.MCPFileKind(values[1]),
		FileID:    values[2],
		UploadID:  values[3],
	}, nil
}

type compactFileTransferResult struct {
	State   filemedia.MCPFileTransferState `json:"s"`
	Session *compactFileSession            `json:"x,omitempty"`
	File    *compactFileHandle             `json:"f,omitempty"`
}

type compactFileSession struct {
	Handle         [4]string                      `json:"h"`
	State          filemedia.MCPFileTransferState `json:"s"`
	FileName       string                         `json:"n,omitempty"`
	MIMEType       string                         `json:"m,omitempty"`
	FileSize       int64                          `json:"z,omitempty"`
	TotalParts     int32                          `json:"p"`
	ChunkSize      int32                          `json:"c"`
	UploadedParts  []int32                        `json:"u"`
	LastActivityAt string                         `json:"a,omitempty"`
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
		Handle: [4]string{
			string(session.Handle.Transport), string(session.Handle.Kind),
			session.Handle.FileID, session.Handle.UploadID,
		},
		State: session.State, FileName: session.FileName, MIMEType: session.MIMEType,
		FileSize: session.FileSize, TotalParts: session.TotalParts, ChunkSize: session.ChunkSize,
		UploadedParts: append([]int32(nil), session.UploadedPartNumbers...),
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
	switch connect.CodeOf(err) {
	case connect.CodeInvalidArgument, connect.CodeNotFound, connect.CodeAlreadyExists,
		connect.CodePermissionDenied, connect.CodeUnauthenticated,
		connect.CodeFailedPrecondition, connect.CodeAborted, connect.CodeOutOfRange:
		return executionError(err)
	default:
		return mcpserver.ToolResult{}, err
	}
}

var (
	_ mcpserver.ToolRegistry   = (*FileTools)(nil)
	_ mcpserver.ToolDispatcher = (*FileTools)(nil)
	_ ToolProvider             = (*FileTools)(nil)
)
