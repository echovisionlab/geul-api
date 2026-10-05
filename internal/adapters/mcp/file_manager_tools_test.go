package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	managev1connect "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const fileManagerTestID = "11111111-1111-4111-8111-111111111111"
const fileManagerTestFolderID = "22222222-2222-4222-8222-222222222222"

func TestFileManagerToolDescriptors(t *testing.T) {
	_, err := NewFileManagerTools(nil)
	require.Error(t, err)
	var missing *recordingFileManager
	_, err = NewFileManagerTools(missing)
	require.Error(t, err)
	tools, err := NewFileManagerTools(&recordingFileManager{})
	require.NoError(t, err)
	listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	require.NoError(t, err)
	require.Len(t, listed, 8)
	for _, tool := range listed {
		assertMCPToolOAuthSecurity(t, tool)
		require.NotContains(t, string(tool.InputSchema), "document_list")
		require.Equal(t, tool.Name == ToolFileDeletionImpactGet, tool.Annotations["readOnlyHint"])
		require.Equal(t, tool.Name == ToolFileDelete || tool.Name == ToolFileFolderDelete, tool.Annotations["destructiveHint"])
		for _, raw := range []json.RawMessage{tool.InputSchema, tool.OutputSchema} {
			var schema map[string]any
			require.NoError(t, json.Unmarshal(raw, &schema))
			require.Equal(t, "object", schema["type"])
			require.Equal(t, false, schema["additionalProperties"])
		}
	}
	for _, name := range []string{ToolFileDelete, ToolFileFolderDelete} {
		for _, tool := range listed {
			if tool.Name == name {
				require.Contains(t, tool.Description, "scheduled, not completed")
				require.Contains(t, tool.Description, "no force or bypass")
			}
		}
	}
	_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, "missing", nil)
	require.ErrorIs(t, err, mcpserver.ErrUnknownTool)
}

func TestFileManagerUUIDSchemasDescribeFileAndFolderDiscovery(t *testing.T) {
	var uuidSchema map[string]any
	require.NoError(t, json.Unmarshal([]byte(uuidJSONSchema), &uuidSchema))
	for _, test := range []struct {
		schema, field, source string
		array, nullable       bool
	}{
		{fileManagerIDsInputJSONSchema, "file_ids", "file_upload", true, false},
		{fileManagerMoveInputJSONSchema, "folder_id", "file_folder_create", false, true},
		{fileManagerRenameInputJSONSchema, "file_id", "file_upload", false, false},
		{fileManagerFolderCreateInputJSONSchema, "parent_id", "file_folder_create", false, true},
		{fileManagerFolderRenameInputJSONSchema, "folder_id", "file_folder_create", false, false},
		{fileManagerFolderMoveInputJSONSchema, "parent_id", "file_folder_create", false, true},
		{fileManagerFolderIDInputJSONSchema, "folder_id", "file_folder_create", false, false},
	} {
		var schema map[string]any
		require.NoError(t, json.Unmarshal([]byte(test.schema), &schema))
		field := schema["properties"].(map[string]any)[test.field].(map[string]any)
		require.Contains(t, field["description"], test.source)
		require.Contains(t, field["description"], "file_list")
		switch {
		case test.array:
			require.Equal(t, uuidSchema, field["items"])
		case test.nullable:
			branches := field["anyOf"].([]any)
			require.Equal(t, uuidSchema, branches[0])
			require.Equal(t, map[string]any{"type": "null"}, branches[1])
		default:
			require.Equal(t, uuidSchema, field["allOf"].([]any)[0])
		}
	}
}

func TestFileManagerCallsExactNativeMethods(t *testing.T) {
	parent := fileManagerTestFolderID
	for _, test := range []struct {
		name, args string
		request    proto.Message
	}{
		{ToolFileDeletionImpactGet, `{"file_ids":["` + fileManagerTestID + `"]}`, &managev1.GetFileDeletionImpactRequest{FileIds: []string{fileManagerTestID}}},
		{ToolFileDelete, `{"file_ids":["` + fileManagerTestID + `"]}`, &managev1.DeleteFilesRequest{FileIds: []string{fileManagerTestID}}},
		{ToolFileRename, `{"file_id":"` + fileManagerTestID + `","file_name":"  New name  "}`, &managev1.RenameFileRequest{FileId: fileManagerTestID, FileName: "  New name  "}},
		{ToolFileMove, `{"file_ids":["` + fileManagerTestID + `"],"folder_id":"` + parent + `"}`, &managev1.MoveFilesRequest{FileIds: []string{fileManagerTestID}, FolderId: &parent}},
		{ToolFileFolderCreate, `{"name":"  New folder  ","parent_id":"` + parent + `"}`, &managev1.CreateFileFolderRequest{Name: "  New folder  ", ParentId: &parent}},
		{ToolFileFolderRename, `{"folder_id":"` + fileManagerTestFolderID + `","name":"Renamed"}`, &managev1.RenameFileFolderRequest{FolderId: fileManagerTestFolderID, Name: "Renamed"}},
		{ToolFileFolderMove, `{"folder_id":"` + fileManagerTestID + `","parent_id":"` + parent + `"}`, &managev1.MoveFileFolderRequest{FolderId: fileManagerTestID, ParentId: &parent}},
		{ToolFileFolderDelete, `{"folder_id":"` + fileManagerTestFolderID + `"}`, &managev1.DeleteFileFolderRequest{FolderId: fileManagerTestFolderID}},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := &recordingFileManager{}
			tools, err := NewFileManagerTools(manager)
			require.NoError(t, err)
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, test.name, toolArguments(t, test.args))
			require.NoError(t, err)
			require.False(t, result.IsError)
			require.Equal(t, []string{test.name}, manager.calls)
			require.Equal(t, t.Context(), manager.ctx)
			require.True(t, proto.Equal(test.request, manager.request))
			if file, ok := result.StructuredContent["file"].(map[string]any); ok {
				require.Equal(t, "Actual name", file["file_name"])
				require.Equal(t, "wav", file["extension"])
				require.Equal(t, nil, file["folder_id"])
				require.Equal(t, float64(12), file["duration_seconds"])
			}
			if folder, ok := result.StructuredContent["folder"].(map[string]any); ok {
				require.Equal(t, "Actual folder", folder["name"])
				require.Equal(t, nil, folder["parent_id"])
			}
		})
	}
}

func TestFileManagerRootDestinations(t *testing.T) {
	for _, test := range []struct{ name, args string }{
		{ToolFileMove, `{"file_ids":["` + fileManagerTestID + `"]}`},
		{ToolFileMove, `{"file_ids":["` + fileManagerTestID + `"],"folder_id":null}`},
		{ToolFileFolderCreate, `{"name":"Folder"}`},
		{ToolFileFolderCreate, `{"name":"Folder","parent_id":null}`},
		{ToolFileFolderMove, `{"folder_id":"` + fileManagerTestFolderID + `"}`},
		{ToolFileFolderMove, `{"folder_id":"` + fileManagerTestFolderID + `","parent_id":null}`},
	} {
		t.Run(test.name+test.args, func(t *testing.T) {
			manager := &recordingFileManager{}
			tools, err := NewFileManagerTools(manager)
			require.NoError(t, err)
			_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, test.name, toolArguments(t, test.args))
			require.NoError(t, err)
			switch request := manager.request.(type) {
			case *managev1.MoveFilesRequest:
				require.Nil(t, request.FolderId)
			case *managev1.CreateFileFolderRequest:
				require.Nil(t, request.ParentId)
			case *managev1.MoveFileFolderRequest:
				require.Nil(t, request.ParentId)
			default:
				t.Fatalf("unexpected request %T", request)
			}
		})
	}
}

func TestFileManagerAllowsHundredIDsWithoutChangingSelection(t *testing.T) {
	ids := make([]string, 100)
	for index := range ids {
		ids[index] = fileManagerTestID
	}
	// Deduplication remains the owning service's responsibility. The MCP cap
	// counts the submitted selection and forwards it unchanged.
	raw, err := json.Marshal(map[string]any{"file_ids": ids})
	require.NoError(t, err)
	for _, name := range []string{ToolFileDelete, ToolFileMove, ToolFileDeletionImpactGet} {
		manager := &recordingFileManager{}
		tools, err := NewFileManagerTools(manager)
		require.NoError(t, err)
		_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, name, toolArguments(t, string(raw)))
		require.NoError(t, err)
		switch request := manager.request.(type) {
		case *managev1.DeleteFilesRequest:
			require.Equal(t, ids, request.FileIds)
		case *managev1.MoveFilesRequest:
			require.Equal(t, ids, request.FileIds)
		case *managev1.GetFileDeletionImpactRequest:
			require.Equal(t, ids, request.FileIds)
		default:
			t.Fatalf("unexpected request %T", request)
		}
	}
}

func TestFileManagerDeletionPreservesAcceptedAndRejectedImpact(t *testing.T) {
	blockID, title, link := fileManagerTestFolderID, "Referenced page", "/pages/reference"
	impact := &managev1.FileDeletionImpact{FileId: fileManagerTestFolderID, TotalUsageCount: 8, HasMoreUsages: true,
		DomainCounts: []*managev1.FileUsageDomainCount{{Domain: managev1.FileUsageDomain_FILE_USAGE_DOMAIN_PAGE, Count: 8}},
		FirstUsages:  []*managev1.FileUsage{{Domain: managev1.FileUsageDomain_FILE_USAGE_DOMAIN_PAGE, EntityId: fileManagerTestID, ReferencePath: "props.file", BlockId: &blockID, Count: 2, Title: &title, Link: &link}}}
	manager := &recordingFileManager{impact: impact, accepted: []string{fileManagerTestID}}
	tools, err := NewFileManagerTools(manager)
	require.NoError(t, err)
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileDelete, toolArguments(t, `{"file_ids":["`+fileManagerTestID+`","`+fileManagerTestFolderID+`"]}`))
	require.NoError(t, err)
	require.Equal(t, []any{fileManagerTestID}, result.StructuredContent["accepted_file_ids"])
	rejected := result.StructuredContent["rejected_files"].([]any)
	require.Len(t, rejected, 1)
	rejection := rejected[0].(map[string]any)
	require.Equal(t, fileManagerTestFolderID, rejection["file_id"])
	require.Equal(t, float64(8), rejection["total_usage_count"])
	require.Equal(t, true, rejection["has_more_usages"])
	require.Equal(t, []any{map[string]any{"domain": "page", "count": float64(8)}}, rejection["domain_counts"])
	usage := rejection["first_usages"].([]any)[0].(map[string]any)
	require.Equal(t, "page", usage["domain"])
	require.Equal(t, "props.file", usage["reference_path"])
	require.Equal(t, blockID, usage["block_id"])
	require.Equal(t, title, usage["title"])
	require.Equal(t, link, usage["link"])
	require.Equal(t, float64(2), usage["count"])
	require.NotContains(t, result.StructuredContent, "success")
	inspected, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileDeletionImpactGet, toolArguments(t, `{"file_ids":["`+fileManagerTestFolderID+`"]}`))
	require.NoError(t, err)
	require.Equal(t, rejected, inspected.StructuredContent["impacts"])
	for _, name := range []string{ToolFileDelete, ToolFileFolderDelete} {
		manager := &recordingFileManager{}
		tools, err := NewFileManagerTools(manager)
		require.NoError(t, err)
		args := `{"file_ids":["` + fileManagerTestID + `"]}`
		if name == ToolFileFolderDelete {
			args = `{"folder_id":"` + fileManagerTestFolderID + `"}`
		}
		result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, name, toolArguments(t, args))
		require.NoError(t, err)
		require.Equal(t, []any{}, result.StructuredContent["accepted_file_ids"])
		if name == ToolFileDelete {
			require.Equal(t, []any{}, result.StructuredContent["rejected_files"])
		}
	}
}

func TestFileManagerRejectsInvalidArgumentsBeforeNativeCall(t *testing.T) {
	tooMany := `{"file_ids":[` + strings.Repeat(`"`+fileManagerTestID+`",`, 100) + `"` + fileManagerTestID + `"]}`
	for _, test := range []struct{ name, args string }{
		{ToolFileDelete, tooMany}, {ToolFileMove, tooMany}, {ToolFileDeletionImpactGet, tooMany},
		{ToolFileDelete, `{}`}, {ToolFileDelete, `{"file_ids":[]}`}, {ToolFileDelete, `{"file_ids":null}`}, {ToolFileDelete, `{"file_ids":[null]}`}, {ToolFileDelete, `{"file_ids":["bad"]}`},
		{ToolFileDelete, `{"file_ids":["` + fileManagerTestID + `"],"force":true}`},
		{ToolFileDelete, `{"file_ids":["` + fileManagerTestID + `"],"folder_id":null}`},
		{ToolFileDeletionImpactGet, `{"file_ids":["` + fileManagerTestID + `"],"folder_id":null}`},
		{ToolFileMove, `{"file_ids":["` + fileManagerTestID + `"],"folder_id":""}`},
		{ToolFileFolderCreate, `{"name":"Folder","parent_id":""}`},
		{ToolFileFolderCreate, `{"name":null}`},
		{ToolFileFolderRename, `{"folder_id":"` + fileManagerTestFolderID + `","name":null}`},
		{ToolFileRename, `{"file_id":"` + fileManagerTestID + `","file_name":null}`},
		{ToolFileRename, `{"file_id":null,"file_name":"Name"}`},
		{ToolFileFolderMove, `{"folder_id":"` + fileManagerTestFolderID + `","parent_id":"bad"}`},
		{ToolFileFolderDelete, `{"folder_id":null}`}, {ToolFileFolderDelete, `{"folder_id":""}`}, {ToolFileFolderDelete, `{"folder_id":"` + fileManagerTestFolderID + `","force":true}`},
	} {
		t.Run(test.name+test.args, func(t *testing.T) {
			manager := &recordingFileManager{}
			tools, err := NewFileManagerTools(manager)
			require.NoError(t, err)
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, test.name, toolArguments(t, test.args))
			var execution *mcpserver.ToolExecutionError
			require.ErrorAs(t, err, &execution)
			require.Nil(t, result.StructuredContent)
			require.Empty(t, manager.calls)
		})
	}
}

func TestFileManagerPreservesNativeErrors(t *testing.T) {
	for _, code := range []connect.Code{connect.CodePermissionDenied, connect.CodeFailedPrecondition, connect.CodeInvalidArgument, connect.CodeNotFound} {
		t.Run(code.String(), func(t *testing.T) {
			manager := &recordingFileManager{err: connect.NewError(code, errors.New("native File Manager rejection"))}
			tools, err := NewFileManagerTools(manager)
			require.NoError(t, err)
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileFolderDelete, toolArguments(t, `{"folder_id":"`+fileManagerTestFolderID+`"}`))
			var execution *mcpserver.ToolExecutionError
			require.ErrorAs(t, err, &execution)
			require.Equal(t, "native File Manager rejection", execution.Message)
			require.Nil(t, result.StructuredContent)
		})
	}
}

type recordingFileManager struct {
	managev1connect.UnimplementedFileServiceHandler
	ctx      context.Context
	request  proto.Message
	calls    []string
	err      error
	impact   *managev1.FileDeletionImpact
	accepted []string
}

func (manager *recordingFileManager) record(ctx context.Context, name string, request proto.Message) {
	manager.ctx = ctx
	manager.request = request
	manager.calls = append(manager.calls, name)
}
func fileManagerNativeFile() *managev1.FileManagerFile {
	duration := int32(12)
	at := timestamppb.New(time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC))
	return &managev1.FileManagerFile{Id: fileManagerTestID, FileName: "Actual name", Extension: "wav", MimeType: "audio/wav", FileSize: 42, DurationSeconds: &duration, UsageCount: 3, CreatedAt: at, UpdatedAt: at}
}
func fileManagerNativeFolder() *managev1.FileFolder {
	return &managev1.FileFolder{Id: fileManagerTestFolderID, Name: "Actual folder"}
}

func (manager *recordingFileManager) GetFileDeletionImpact(ctx context.Context, request *connect.Request[managev1.GetFileDeletionImpactRequest]) (*connect.Response[managev1.GetFileDeletionImpactResponse], error) {
	manager.record(ctx, ToolFileDeletionImpactGet, request.Msg)
	if manager.err != nil {
		return nil, manager.err
	}
	impacts := []*managev1.FileDeletionImpact{}
	if manager.impact != nil {
		impacts = append(impacts, manager.impact)
	}
	return connect.NewResponse(&managev1.GetFileDeletionImpactResponse{Impacts: impacts}), nil
}

func (manager *recordingFileManager) DeleteFiles(ctx context.Context, request *connect.Request[managev1.DeleteFilesRequest]) (*connect.Response[managev1.DeleteFilesResponse], error) {
	manager.record(ctx, ToolFileDelete, request.Msg)
	if manager.err != nil {
		return nil, manager.err
	}
	impacts := []*managev1.FileDeletionImpact{}
	if manager.impact != nil {
		impacts = append(impacts, manager.impact)
	}
	return connect.NewResponse(&managev1.DeleteFilesResponse{AcceptedFileIds: manager.accepted, RejectedFiles: impacts}), nil
}

func (manager *recordingFileManager) RenameFile(ctx context.Context, request *connect.Request[managev1.RenameFileRequest]) (*connect.Response[managev1.RenameFileResponse], error) {
	manager.record(ctx, ToolFileRename, request.Msg)
	if manager.err != nil {
		return nil, manager.err
	}
	return connect.NewResponse(&managev1.RenameFileResponse{File: fileManagerNativeFile()}), nil
}

func (manager *recordingFileManager) MoveFiles(ctx context.Context, request *connect.Request[managev1.MoveFilesRequest]) (*connect.Response[managev1.MoveFilesResponse], error) {
	manager.record(ctx, ToolFileMove, request.Msg)
	if manager.err != nil {
		return nil, manager.err
	}
	return connect.NewResponse(&managev1.MoveFilesResponse{Files: []*managev1.FileManagerFile{fileManagerNativeFile()}}), nil
}

func (manager *recordingFileManager) CreateFileFolder(ctx context.Context, request *connect.Request[managev1.CreateFileFolderRequest]) (*connect.Response[managev1.CreateFileFolderResponse], error) {
	manager.record(ctx, ToolFileFolderCreate, request.Msg)
	if manager.err != nil {
		return nil, manager.err
	}
	return connect.NewResponse(&managev1.CreateFileFolderResponse{Folder: fileManagerNativeFolder()}), nil
}

func (manager *recordingFileManager) RenameFileFolder(ctx context.Context, request *connect.Request[managev1.RenameFileFolderRequest]) (*connect.Response[managev1.RenameFileFolderResponse], error) {
	manager.record(ctx, ToolFileFolderRename, request.Msg)
	if manager.err != nil {
		return nil, manager.err
	}
	return connect.NewResponse(&managev1.RenameFileFolderResponse{Folder: fileManagerNativeFolder()}), nil
}

func (manager *recordingFileManager) MoveFileFolder(ctx context.Context, request *connect.Request[managev1.MoveFileFolderRequest]) (*connect.Response[managev1.MoveFileFolderResponse], error) {
	manager.record(ctx, ToolFileFolderMove, request.Msg)
	if manager.err != nil {
		return nil, manager.err
	}
	return connect.NewResponse(&managev1.MoveFileFolderResponse{Folder: fileManagerNativeFolder()}), nil
}

func (manager *recordingFileManager) DeleteFileFolder(ctx context.Context, request *connect.Request[managev1.DeleteFileFolderRequest]) (*connect.Response[managev1.DeleteFileFolderResponse], error) {
	manager.record(ctx, ToolFileFolderDelete, request.Msg)
	if manager.err != nil {
		return nil, manager.err
	}
	return connect.NewResponse(&managev1.DeleteFileFolderResponse{AcceptedFileIds: manager.accepted}), nil
}
