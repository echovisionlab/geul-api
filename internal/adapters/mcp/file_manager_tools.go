package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const (
	ToolFileDeletionImpactGet = "file_deletion_impact_get"
	ToolFileDelete            = "file_delete"
	ToolFileRename            = "file_rename"
	ToolFileMove              = "file_move"
	ToolFileFolderCreate      = "file_folder_create"
	ToolFileFolderRename      = "file_folder_rename"
	ToolFileFolderMove        = "file_folder_move"
	ToolFileFolderDelete      = "file_folder_delete"
)

// FileManager consumes the native administrator File Manager boundary. The
// owning service retains fresh authorization, no-op handling and durable audit.
type FileManager interface {
	GetFileDeletionImpact(context.Context, *connect.Request[managev1.GetFileDeletionImpactRequest]) (*connect.Response[managev1.GetFileDeletionImpactResponse], error)
	DeleteFiles(context.Context, *connect.Request[managev1.DeleteFilesRequest]) (*connect.Response[managev1.DeleteFilesResponse], error)
	RenameFile(context.Context, *connect.Request[managev1.RenameFileRequest]) (*connect.Response[managev1.RenameFileResponse], error)
	MoveFiles(context.Context, *connect.Request[managev1.MoveFilesRequest]) (*connect.Response[managev1.MoveFilesResponse], error)
	CreateFileFolder(context.Context, *connect.Request[managev1.CreateFileFolderRequest]) (*connect.Response[managev1.CreateFileFolderResponse], error)
	RenameFileFolder(context.Context, *connect.Request[managev1.RenameFileFolderRequest]) (*connect.Response[managev1.RenameFileFolderResponse], error)
	MoveFileFolder(context.Context, *connect.Request[managev1.MoveFileFolderRequest]) (*connect.Response[managev1.MoveFileFolderResponse], error)
	DeleteFileFolder(context.Context, *connect.Request[managev1.DeleteFileFolderRequest]) (*connect.Response[managev1.DeleteFileFolderResponse], error)
}

var fileManagerTools = []mcpserver.Tool{
	oauthTool(ToolFileDeletionImpactGet, "Inspect File deletion impact", "Inspect current usage counts and up to five exact usages per File through administrator File authority. Use file_usage_list for remaining usages; references can change before deletion.", fileManagerIDsInputJSONSchema, fileManagerImpactOutputJSONSchema, true, false),
	oauthTool(ToolFileDelete, "Delete Files", "Request durable deletion of up to 100 Files through administrator File authority. accepted_file_ids means deletion is scheduled, not completed. Referenced Files are rejected with their current impact; no force or bypass is supported.", fileManagerIDsInputJSONSchema, fileManagerDeleteOutputJSONSchema, false, true),
	oauthTool(ToolFileRename, "Rename a File", "Rename a File through administrator File authority. file_name excludes its extension. Returns actual current metadata; file_read provides delivery references.", fileManagerRenameInputJSONSchema, fileManagerFileOutputJSONSchema, false, false),
	oauthTool(ToolFileMove, "Move Files", "Move up to 100 Files through administrator File authority. Omit folder_id or pass null for the virtual root. Returns actual current metadata; file_read provides delivery references.", fileManagerMoveInputJSONSchema, fileManagerFilesOutputJSONSchema, false, false),
	oauthTool(ToolFileFolderCreate, "Create a File folder", "Create a logical File Manager folder through administrator File authority. Omit parent_id or pass null for the virtual root; names must exclude path separators.", fileManagerFolderCreateInputJSONSchema, fileManagerFolderOutputJSONSchema, false, false),
	oauthTool(ToolFileFolderRename, "Rename a File folder", "Rename a logical File Manager folder through administrator File authority. Native duplicate-name and no-op rules apply.", fileManagerFolderRenameInputJSONSchema, fileManagerFolderOutputJSONSchema, false, false),
	oauthTool(ToolFileFolderMove, "Move a File folder", "Move a logical File Manager folder through administrator File authority. Omit parent_id or pass null for the virtual root. Native hierarchy, cycle and duplicate-name rules apply.", fileManagerFolderMoveInputJSONSchema, fileManagerFolderOutputJSONSchema, false, false),
	oauthTool(ToolFileFolderDelete, "Delete a File folder", "Delete a folder and its descendant folders through administrator File authority, scheduling their contained Files for durable deletion. accepted_file_ids means scheduled, not completed. Any active File reference rejects the entire operation; no force or bypass is supported. The virtual root cannot be deleted.", fileManagerFolderIDInputJSONSchema, fileManagerFolderDeleteOutputJSONSchema, false, true),
}

type FileManagerTools struct{ files FileManager }

func NewFileManagerTools(files FileManager) (*FileManagerTools, error) {
	if interfaceValueIsNil(files) {
		return nil, errors.New("MCP File Manager is required")
	}
	return &FileManagerTools{files: files}, nil
}
func (*FileManagerTools) ToolNames() []string { return toolDefinitionNames(fileManagerTools) }
func (*FileManagerTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return cloneToolDefinitions(fileManagerTools), nil
}

func (tools *FileManagerTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, args mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	switch name {
	case ToolFileDeletionImpactGet:
		return tools.getDeletionImpact(ctx, args)
	case ToolFileDelete:
		return tools.deleteFiles(ctx, args)
	case ToolFileRename:
		return tools.renameFile(ctx, args)
	case ToolFileMove:
		return tools.moveFiles(ctx, args)
	case ToolFileFolderCreate:
		return tools.createFolder(ctx, args)
	case ToolFileFolderRename:
		return tools.renameFolder(ctx, args)
	case ToolFileFolderMove:
		return tools.moveFolder(ctx, args)
	case ToolFileFolderDelete:
		return tools.deleteFolder(ctx, args)
	default:
		return mcpserver.ToolResult{}, mcpserver.ErrUnknownTool
	}
}

type fileManagerSelectionArguments struct {
	FileIDs []string `json:"file_ids"`
}

func (tools *FileManagerTools) getDeletionImpact(ctx context.Context, args mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input fileManagerSelectionArguments
	if err := decodeArguments(args, &input); err != nil {
		return executionError(err)
	}
	if err := validateFileManagerIDs(input.FileIDs); err != nil {
		return executionError(err)
	}
	response, err := tools.files.GetFileDeletionImpact(ctx, connect.NewRequest(&managev1.GetFileDeletionImpactRequest{FileIds: input.FileIDs}))
	if err != nil {
		return expectedToolError(err)
	}
	impacts, err := projectFileManagerImpacts(response.Msg.Impacts)
	if err != nil {
		return mcpserver.ToolResult{}, err
	}
	return fileManagerResult(struct {
		Impacts []fileManagerImpact `json:"impacts"`
	}{impacts})
}

func (tools *FileManagerTools) deleteFiles(ctx context.Context, args mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input fileManagerSelectionArguments
	if err := decodeArguments(args, &input); err != nil {
		return executionError(err)
	}
	if err := validateFileManagerIDs(input.FileIDs); err != nil {
		return executionError(err)
	}
	response, err := tools.files.DeleteFiles(ctx, connect.NewRequest(&managev1.DeleteFilesRequest{FileIds: input.FileIDs}))
	if err != nil {
		return expectedToolError(err)
	}
	rejected, err := projectFileManagerImpacts(response.Msg.RejectedFiles)
	if err != nil {
		return mcpserver.ToolResult{}, err
	}
	return fileManagerResult(struct {
		Accepted []string            `json:"accepted_file_ids"`
		Rejected []fileManagerImpact `json:"rejected_files"`
	}{append([]string{}, response.Msg.AcceptedFileIds...), rejected})
}

func (tools *FileManagerTools) moveFiles(ctx context.Context, args mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input struct {
		FileIDs  []string `json:"file_ids"`
		FolderID *string  `json:"folder_id"`
	}
	if err := decodeArguments(args, &input); err != nil {
		return executionError(err)
	}
	if err := validateFileManagerIDs(input.FileIDs); err != nil {
		return executionError(err)
	}
	if input.FolderID != nil {
		if err := validateUUID("folder_id", *input.FolderID); err != nil {
			return executionError(err)
		}
	}
	response, err := tools.files.MoveFiles(ctx, connect.NewRequest(&managev1.MoveFilesRequest{FileIds: input.FileIDs, FolderId: input.FolderID}))
	if err != nil {
		return expectedToolError(err)
	}
	files := make([]fileManagerFile, 0, len(response.Msg.Files))
	for _, file := range response.Msg.Files {
		files = append(files, projectFileManagerFile(file))
	}
	return fileManagerResult(struct {
		Files []fileManagerFile `json:"files"`
	}{files})
}

func (tools *FileManagerTools) renameFile(ctx context.Context, args mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input struct {
		FileID string `json:"file_id"`
		Name   string `json:"file_name"`
	}
	if err := decodeArguments(args, &input); err != nil {
		return executionError(err)
	}
	if err := rejectNullArguments(args, "file_name"); err != nil {
		return executionError(err)
	}
	if err := validateUUID("file_id", input.FileID); err != nil {
		return executionError(err)
	}
	response, err := tools.files.RenameFile(ctx, connect.NewRequest(&managev1.RenameFileRequest{FileId: input.FileID, FileName: input.Name}))
	if err != nil {
		return expectedToolError(err)
	}
	return fileManagerResult(struct {
		File fileManagerFile `json:"file"`
	}{projectFileManagerFile(response.Msg.File)})
}

func (tools *FileManagerTools) createFolder(ctx context.Context, args mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input struct {
		ParentID *string `json:"parent_id"`
		Name     string  `json:"name"`
	}
	if err := decodeArguments(args, &input); err != nil {
		return executionError(err)
	}
	if err := rejectNullArguments(args, "name"); err != nil {
		return executionError(err)
	}
	if input.ParentID != nil {
		if err := validateUUID("parent_id", *input.ParentID); err != nil {
			return executionError(err)
		}
	}
	response, err := tools.files.CreateFileFolder(ctx, connect.NewRequest(&managev1.CreateFileFolderRequest{ParentId: input.ParentID, Name: input.Name}))
	if err != nil {
		return expectedToolError(err)
	}
	return fileManagerResult(struct {
		Folder fileManagerFolder `json:"folder"`
	}{projectFileManagerFolder(response.Msg.Folder)})
}

func (tools *FileManagerTools) renameFolder(ctx context.Context, args mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input struct {
		FolderID string `json:"folder_id"`
		Name     string `json:"name"`
	}
	if err := decodeArguments(args, &input); err != nil {
		return executionError(err)
	}
	if err := rejectNullArguments(args, "name"); err != nil {
		return executionError(err)
	}
	if err := validateUUID("folder_id", input.FolderID); err != nil {
		return executionError(err)
	}
	response, err := tools.files.RenameFileFolder(ctx, connect.NewRequest(&managev1.RenameFileFolderRequest{FolderId: input.FolderID, Name: input.Name}))
	if err != nil {
		return expectedToolError(err)
	}
	return fileManagerResult(struct {
		Folder fileManagerFolder `json:"folder"`
	}{projectFileManagerFolder(response.Msg.Folder)})
}

func (tools *FileManagerTools) moveFolder(ctx context.Context, args mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input struct {
		FolderID string  `json:"folder_id"`
		ParentID *string `json:"parent_id"`
	}
	if err := decodeArguments(args, &input); err != nil {
		return executionError(err)
	}
	if err := validateUUID("folder_id", input.FolderID); err != nil {
		return executionError(err)
	}
	if input.ParentID != nil {
		if err := validateUUID("parent_id", *input.ParentID); err != nil {
			return executionError(err)
		}
	}
	response, err := tools.files.MoveFileFolder(ctx, connect.NewRequest(&managev1.MoveFileFolderRequest{FolderId: input.FolderID, ParentId: input.ParentID}))
	if err != nil {
		return expectedToolError(err)
	}
	return fileManagerResult(struct {
		Folder fileManagerFolder `json:"folder"`
	}{projectFileManagerFolder(response.Msg.Folder)})
}

func (tools *FileManagerTools) deleteFolder(ctx context.Context, args mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input struct {
		FolderID string `json:"folder_id"`
	}
	if err := decodeArguments(args, &input); err != nil {
		return executionError(err)
	}
	if err := validateUUID("folder_id", input.FolderID); err != nil {
		return executionError(err)
	}
	response, err := tools.files.DeleteFileFolder(ctx, connect.NewRequest(&managev1.DeleteFileFolderRequest{FolderId: input.FolderID}))
	if err != nil {
		return expectedToolError(err)
	}
	return fileManagerResult(struct {
		FolderID string   `json:"folder_id"`
		Accepted []string `json:"accepted_file_ids"`
	}{input.FolderID, append([]string{}, response.Msg.AcceptedFileIds...)})
}

func validateFileManagerIDs(ids []string) error {
	if len(ids) < 1 || len(ids) > 100 {
		return errors.New("file_ids must contain 1 to 100 File UUIDs")
	}
	for _, id := range ids {
		if err := validateUUID("file_ids", id); err != nil {
			return err
		}
	}
	return nil
}

type fileManagerFile struct {
	ID         string  `json:"id"`
	Name       string  `json:"file_name"`
	Extension  string  `json:"extension"`
	MIMEType   string  `json:"mime_type"`
	Size       int64   `json:"file_size"`
	Duration   *int32  `json:"duration_seconds,omitempty"`
	FolderID   *string `json:"folder_id"`
	UsageCount int32   `json:"usage_count"`
	CreatedAt  string  `json:"created_at"`
	UpdatedAt  string  `json:"updated_at"`
}

func projectFileManagerFile(file *managev1.FileManagerFile) fileManagerFile {
	return fileManagerFile{ID: file.GetId(), Name: file.GetFileName(), Extension: file.GetExtension(), MIMEType: file.GetMimeType(), Size: file.GetFileSize(), Duration: file.DurationSeconds, FolderID: file.FolderId, UsageCount: file.GetUsageCount(), CreatedAt: timestampString(file.GetCreatedAt()), UpdatedAt: timestampString(file.GetUpdatedAt())}
}

type fileManagerFolder struct {
	ID        string  `json:"id"`
	ParentID  *string `json:"parent_id"`
	Name      string  `json:"name"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

func projectFileManagerFolder(folder *managev1.FileFolder) fileManagerFolder {
	return fileManagerFolder{ID: folder.GetId(), ParentID: folder.ParentId, Name: folder.GetName(), CreatedAt: timestampString(folder.GetCreatedAt()), UpdatedAt: timestampString(folder.GetUpdatedAt())}
}

type fileManagerUsage struct {
	Domain        string  `json:"domain"`
	EntityID      string  `json:"entity_id"`
	ReferencePath string  `json:"reference_path"`
	BlockID       *string `json:"block_id,omitempty"`
	Count         int32   `json:"count"`
	BlockType     *string `json:"block_type,omitempty"`
	Title         *string `json:"title,omitempty"`
	Link          *string `json:"link,omitempty"`
}
type fileManagerDomainCount struct {
	Domain string `json:"domain"`
	Count  int64  `json:"count"`
}
type fileManagerImpact struct {
	FileID       string                   `json:"file_id"`
	Total        int64                    `json:"total_usage_count"`
	DomainCounts []fileManagerDomainCount `json:"domain_counts"`
	FirstUsages  []fileManagerUsage       `json:"first_usages"`
	HasMore      bool                     `json:"has_more_usages"`
}

func projectFileManagerImpacts(impacts []*managev1.FileDeletionImpact) ([]fileManagerImpact, error) {
	result := make([]fileManagerImpact, 0, len(impacts))
	for _, impact := range impacts {
		item := fileManagerImpact{FileID: impact.FileId, Total: impact.TotalUsageCount, DomainCounts: make([]fileManagerDomainCount, 0, len(impact.DomainCounts)), FirstUsages: make([]fileManagerUsage, 0, len(impact.FirstUsages)), HasMore: impact.HasMoreUsages}
		for _, count := range impact.DomainCounts {
			domain, err := fileUsageDomainName(count.Domain)
			if err != nil {
				return nil, err
			}
			item.DomainCounts = append(item.DomainCounts, fileManagerDomainCount{domain, count.Count})
		}
		for _, usage := range impact.FirstUsages {
			domain, err := fileUsageDomainName(usage.Domain)
			if err != nil {
				return nil, err
			}
			item.FirstUsages = append(item.FirstUsages, fileManagerUsage{domain, usage.EntityId, usage.ReferencePath, usage.BlockId, usage.Count, usage.BlockType, usage.Title, usage.Link})
		}
		result = append(result, item)
	}
	return result, nil
}
func fileManagerResult(value any) (mcpserver.ToolResult, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return mcpserver.ToolResult{}, fmt.Errorf("encode MCP File Manager result: %w", err)
	}
	return structuredResult(raw, false)
}
