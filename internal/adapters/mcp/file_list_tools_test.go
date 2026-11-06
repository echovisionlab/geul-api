package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia"

	"connectrpc.com/connect"

	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestFileListToolDescriptors(t *testing.T) {
	tools := newRecordingFileListTools(t)
	listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("listed %d File tools", len(listed))
	}
	for _, tool := range listed {
		assertMCPToolOAuthSecurity(t, tool)
		if tool.Annotations["readOnlyHint"] != true || tool.Annotations["destructiveHint"] != false || tool.Annotations["openWorldHint"] != false {
			t.Fatalf("%s annotations = %#v", tool.Name, tool.Annotations)
		}
		for _, schema := range []json.RawMessage{tool.InputSchema, tool.OutputSchema} {
			var object map[string]any
			if err := json.Unmarshal(schema, &object); err != nil || object["type"] != "object" {
				t.Fatalf("%s invalid schema: %v", tool.Name, err)
			}
		}
	}
}

func TestFileListReturnsCanonicalFileAndFolderIDs(t *testing.T) {
	files := &recordingFileReferences{}
	tools, err := NewFileListTools(files)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileList, toolArguments(t, `{"query":"cover","mime_type_prefix":"image/","page_size":10}`))
	if err != nil {
		t.Fatal(err)
	}
	if files.request.Msg.Query == nil || *files.request.Msg.Query != "cover" || files.request.Msg.PageSize != 10 || files.request.Msg.SortField != managev1.FileManagerSortField_FILE_MANAGER_SORT_FIELD_UPDATED_AT {
		t.Fatalf("file request = %#v", files.request.Msg)
	}
	items := result.StructuredContent["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["item_type"] != "file" || items[1].(map[string]any)["item_type"] != "folder" {
		t.Fatalf("file result = %#v", result.StructuredContent)
	}
}

func TestFileListEnforcesPageSizeBeforeCallingApplication(t *testing.T) {
	for _, test := range []struct {
		name     string
		payload  string
		pageSize int32
		invalid  bool
	}{
		{name: "omitted", payload: `{}`, pageSize: 20},
		{name: "minimum", payload: `{"page_size":1}`, pageSize: 1},
		{name: "maximum", payload: `{"page_size":100}`, pageSize: 100},
		{name: "zero", payload: `{"page_size":0}`, invalid: true},
		{name: "negative", payload: `{"page_size":-1}`, invalid: true},
		{name: "above maximum", payload: `{"page_size":101}`, invalid: true},
		{name: "native directory maximum", payload: `{"page_size":10000}`, invalid: true},
		{name: "null", payload: `{"page_size":null}`, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := &recordingFileReferences{}
			tools, err := NewFileListTools(files)
			if err != nil {
				t.Fatal(err)
			}
			_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileList, toolArguments(t, test.payload))
			if test.invalid {
				var executionErr *mcpserver.ToolExecutionError
				if !errors.As(err, &executionErr) || files.request != nil {
					t.Fatalf("invalid page_size reached application: err=%v request=%+v", err, files.request)
				}
				return
			}
			if err != nil || files.request == nil || files.request.Msg.PageSize != test.pageSize {
				t.Fatalf("page_size request=%+v, err=%v, want=%d", files.request, err, test.pageSize)
			}
		})
	}
}

func newRecordingFileListTools(t *testing.T) *FileListTools {
	t.Helper()
	tools, err := NewFileListTools(&recordingFileReferences{})
	if err != nil {
		t.Fatal(err)
	}
	return tools
}

type recordingFileReferences struct {
	request *connect.Request[managev1.ListFileManagerItemsRequest]
}

// ListFileManagerItems records the query as the request a Connect client
// would have sent, so the assertions read the native field names.
func (r *recordingFileReferences) ListFileManagerItems(_ context.Context, query filemedia.ListFileManagerItemsQuery) (filemedia.FileManagerItemPage, error) {
	optional := func(value string) *string {
		if value == "" {
			return nil
		}
		return &value
	}
	r.request = connect.NewRequest(&managev1.ListFileManagerItemsRequest{
		FolderId: optional(query.FolderID), Query: optional(query.Query), MimeTypePrefix: optional(query.MimeTypePrefix),
		UploadedByMemberId: optional(query.UploadedByMemberID), SortField: managev1.FileManagerSortField(query.SortField), SortOrder: commonv1.SortOrder(query.SortOrder),

		PageSize: query.PageSize, PageToken: optional(query.PageToken),
	})
	now := time.Now()
	return filemedia.FileManagerItemPage{Items: []*filemedia.LibraryItem{
		{Type: filemedia.LibraryItemFile, File: &filemedia.LibraryFile{ID: "22222222-2222-4222-8222-222222222222", FileName: "cover.png", MimeType: "image/png", CreatedAt: &now}},
		{Type: filemedia.LibraryItemFolder, Folder: &filemedia.FolderView{ID: "33333333-3333-4333-8333-333333333333", Name: "Covers", CreatedAt: &now}},
	}, Total: 2}, nil
}
