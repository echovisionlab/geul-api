package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestReferenceDiscoveryToolDescriptors(t *testing.T) {
	tools := newRecordingReferenceDiscoveryTools(t)
	listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Fatalf("listed %d reference tools", len(listed))
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

func TestReferenceSearchBuildsExactCategoryRequest(t *testing.T) {
	categories := &recordingCategoryReferences{}
	tools, err := NewReferenceDiscoveryTools(categories, &recordingTagReferences{}, &recordingClientReferences{}, &recordingMapPlaceReferences{}, &recordingMemberReferences{}, &recordingFileReferences{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolReferenceSearch, toolArguments(t, `{"reference_type":"category","query":"Sound","limit":12}`))
	if err != nil {
		t.Fatal(err)
	}
	if categories.request.Msg.Pagination.Limit != 12 || len(categories.request.Msg.Filters) != 1 || categories.request.Msg.Filters[0].Field != "search" || categories.request.Msg.Filters[0].Value != "Sound" {
		t.Fatalf("category request = %#v", categories.request.Msg)
	}
	items := result.StructuredContent["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("category result = %#v", result.StructuredContent)
	}
}

func TestFileListReturnsCanonicalFileAndFolderIDs(t *testing.T) {
	files := &recordingFileReferences{}
	tools, err := NewReferenceDiscoveryTools(&recordingCategoryReferences{}, &recordingTagReferences{}, &recordingClientReferences{}, &recordingMapPlaceReferences{}, &recordingMemberReferences{}, files)
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

func newRecordingReferenceDiscoveryTools(t *testing.T) *ReferenceDiscoveryTools {
	t.Helper()
	tools, err := NewReferenceDiscoveryTools(&recordingCategoryReferences{}, &recordingTagReferences{}, &recordingClientReferences{}, &recordingMapPlaceReferences{}, &recordingMemberReferences{}, &recordingFileReferences{})
	if err != nil {
		t.Fatal(err)
	}
	return tools
}

type recordingCategoryReferences struct {
	managev1connect.UnimplementedCategoryServiceHandler
	request *connect.Request[managev1.ListCategoriesRequest]
}

func (r *recordingCategoryReferences) ListCategories(_ context.Context, request *connect.Request[managev1.ListCategoriesRequest]) (*connect.Response[managev1.ListCategoriesResponse], error) {
	r.request = request
	slug := "sound"
	return connect.NewResponse(&managev1.ListCategoriesResponse{Categories: []*managev1.Category{{Id: "11111111-1111-4111-8111-111111111111", Name: "Sound", Slug: &slug}}, Pagination: &commonv1.PaginationResponse{Total: 1, Limit: request.Msg.Pagination.Limit}}), nil
}

type recordingTagReferences struct {
	managev1connect.UnimplementedTagServiceHandler
}
type recordingClientReferences struct {
	managev1connect.UnimplementedClientServiceHandler
}
type recordingMapPlaceReferences struct {
	managev1connect.UnimplementedMapPlaceServiceHandler
}
type recordingMemberReferences struct {
	managev1connect.UnimplementedMemberServiceHandler
}
type recordingFileReferences struct {
	managev1connect.UnimplementedFileServiceHandler
	request *connect.Request[managev1.ListFileManagerItemsRequest]
}

func (r *recordingFileReferences) ListFileManagerItems(_ context.Context, request *connect.Request[managev1.ListFileManagerItemsRequest]) (*connect.Response[managev1.ListFileManagerItemsResponse], error) {
	r.request = request
	now := timestamppb.Now()
	return connect.NewResponse(&managev1.ListFileManagerItemsResponse{Items: []*managev1.FileManagerItem{
		{Item: &managev1.FileManagerItem_File{File: &managev1.FileManagerFile{Id: "22222222-2222-4222-8222-222222222222", FileName: "cover.png", MimeType: "image/png", CreatedAt: now}}},
		{Item: &managev1.FileManagerItem_Folder{Folder: &managev1.FileFolder{Id: "33333333-3333-4333-8333-333333333333", Name: "Covers", CreatedAt: now}}},
	}, Total: 2}), nil
}
