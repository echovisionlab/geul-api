package mcp

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/filemedia"

	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
)

const ToolFileList = "file_list"

const fileListInputJSONSchema = `{
  "type":"object","additionalProperties":false,
  "properties":{"query":{"type":"string"},"folder_id":{"description":"Folder UUID from a file_list item with item_type=folder.","allOf":[` + uuidJSONSchema + `]},"mime_type_prefix":{"type":"string"},"page_size":{"type":"integer","minimum":1,"maximum":100,"default":20},"page_token":{"type":"string"}}
}`

const fileListOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["items","total"],
  "properties":{
    "items":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["item_type","id","name","created_at"],"properties":{"item_type":{"enum":["file","folder"]},"id":` + uuidJSONSchema + `,"name":{"type":"string"},"mime_type":{"type":"string"},"file_size":{"type":"integer"},"folder_id":{"type":"string"},"usage_count":{"type":"integer"},"created_at":{"type":"string","format":"date-time"},"folder_path":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["id","name"],"properties":{"id":` + uuidJSONSchema + `,"name":{"type":"string"}}}}}}},
    "total":{"type":"integer"},"next_page_token":{"type":"string"}
  }
}`

var fileListTools = toolRegistry[FileListTools]{
	defineTool(oauthTool(ToolFileList, "List or search Files", "Browse a File Manager folder or search Files and folders. Use returned File IDs for featured images and other file relations.", fileListInputJSONSchema, fileListOutputJSONSchema, true, false), (*FileListTools).listFiles),
}

type FileReferenceDiscovery interface {
	ListFileManagerItems(context.Context, filemedia.ListFileManagerItemsQuery) (filemedia.FileManagerItemPage, error)
}

// FileListTools browses and searches the File Manager.
type FileListTools struct {
	files FileReferenceDiscovery
}

func NewFileListTools(files FileReferenceDiscovery) (*FileListTools, error) {
	if interfaceValueIsNil(files) {
		return nil, errors.New("MCP File list needs the File Manager application")
	}
	return &FileListTools{files: files}, nil
}

func (*FileListTools) ToolNames() []string { return fileListTools.names() }

func (*FileListTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return fileListTools.list(), nil
}

func (tools *FileListTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	return fileListTools.call(tools, ctx, name, arguments)
}

type fileListArguments struct {
	Query          *string `json:"query,omitempty"`
	FolderID       *string `json:"folder_id,omitempty"`
	MIMETypePrefix *string `json:"mime_type_prefix,omitempty"`
	PageSize       int32   `json:"page_size,omitempty"`
	PageToken      *string `json:"page_token,omitempty"`
}

func (tools *FileListTools) listFiles(ctx context.Context, _ string, call toolCall[fileListArguments]) (mcpserver.ToolResult, error) {
	arguments := call.Arguments

	if err := rejectNullArguments(arguments, "page_size"); err != nil {
		return executionError(err)
	}
	var input fileListArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if _, supplied := arguments["page_size"]; !supplied {
		input.PageSize = 20
	}
	if input.PageSize < 1 || input.PageSize > 100 {
		return executionError(errors.New("page_size must be between 1 and 100, or omitted for the default"))
	}
	response, err := tools.files.ListFileManagerItems(ctx, filemedia.ListFileManagerItemsQuery{
		FolderID: optionalString(input.FolderID), Query: optionalString(input.Query), MimeTypePrefix: optionalString(input.MIMETypePrefix),
		SortField: filemedia.LibrarySortUpdatedAt,
		SortOrder: filemedia.SortDesc, PageSize: input.PageSize, PageToken: optionalString(input.PageToken),
	})
	if err != nil {
		return expectedToolError(err)
	}
	items := make([]map[string]any, 0, len(response.Items))
	for _, item := range response.Items {
		if item == nil {
			continue
		}
		path := make([]map[string]any, 0, len(item.FolderPath))
		for _, segment := range item.FolderPath {
			if segment != nil {
				path = append(path, map[string]any{"id": segment.ID, "name": segment.Name})
			}
		}
		if file := item.File; file != nil {
			output := map[string]any{"item_type": "file", "id": file.ID, "name": file.FileName, "mime_type": file.MimeType, "file_size": file.FileSize, "usage_count": file.UsageCount, "created_at": fileManagerTimeString(file.CreatedAt), "folder_path": path}
			if file.FolderID != nil {
				output["folder_id"] = *file.FolderID
			}
			items = append(items, output)
			continue
		}
		if folder := item.Folder; folder != nil {
			output := map[string]any{"item_type": "folder", "id": folder.ID, "name": folder.Name, "created_at": fileManagerTimeString(folder.CreatedAt), "folder_path": path}
			if folder.ParentID != nil {
				output["folder_id"] = *folder.ParentID
			}
			items = append(items, output)
		}
	}
	return contentResult(map[string]any{"items": items, "total": response.Total, "next_page_token": optionalStringValue(response.NextPageToken)})
}

var _ ToolProvider = (*FileListTools)(nil)
