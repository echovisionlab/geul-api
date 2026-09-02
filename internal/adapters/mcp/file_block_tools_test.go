package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect"
	core "github.com/echovisionlab/geul-api/internal/aidocument"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const (
	fileBlockTestDocumentID = "44444444-4444-4444-8444-444444444444"
	fileBlockTestBlockID    = "55555555-5555-4555-8555-555555555555"
	fileBlockTestFileID     = "66666666-6666-4666-8666-666666666666"
	fileBlockReplacementID  = "77777777-7777-4777-8777-777777777777"
	fileBlockSegmentID      = "88888888-8888-4888-8888-888888888888"
)

func TestFileBlockToolDescriptorsAreFocusedAndAnnotated(t *testing.T) {
	tools := mustFileBlockTools(t, &recordingAIDocumentApplication{}, &recordingFileBlockManagement{})
	listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		name        string
		annotations map[string]any
	}{
		{ToolDocumentFileAdd, toolAnnotations(false, false, false)},
		{ToolDocumentFileReplace, toolAnnotations(false, true, false)},
		{ToolDocumentFileRemove, toolAnnotations(false, true, false)},
		{ToolDocumentFileDownloadPolicyGet, toolAnnotations(true, false, false)},
		{ToolDocumentFileDownloadPolicyUpdate, toolAnnotations(false, true, true)},
		{ToolFileUsageList, toolAnnotations(true, false, false)},
	}
	if len(listed) != len(want) {
		t.Fatalf("ListTools() returned %d tools, want %d", len(listed), len(want))
	}
	for index, definition := range want {
		tool := listed[index]
		if tool.Name != definition.name {
			t.Fatalf("tool %d = %q, want %q", index, tool.Name, definition.name)
		}
		assertMCPToolOAuthSecurity(t, tool)
		assertMCPToolAnnotations(t, tool, definition.annotations)
		for _, schema := range []json.RawMessage{tool.InputSchema, tool.OutputSchema} {
			var object map[string]any
			if err := json.Unmarshal(schema, &object); err != nil || object["type"] != "object" {
				t.Fatalf("%s schema is invalid: %s (%v)", tool.Name, schema, err)
			}
		}
	}
	getSchema := string(listed[3].InputSchema)
	if strings.Contains(getSchema, "reference_path") || strings.Contains(getSchema, "file_id") {
		t.Fatalf("policy get schema lets callers assert relation authority: %s", getSchema)
	}
	updateSchema := string(listed[4].InputSchema)
	if strings.Contains(updateSchema, "reference_path") || !strings.Contains(updateSchema, "expected_file_id") {
		t.Fatalf("policy update schema does not expose only the File CAS: %s", updateSchema)
	}
}

func TestDocumentFileAddReusesExistingFileWithDocumentCAS(t *testing.T) {
	application := &recordingAIDocumentApplication{applyResult: core.ApplyResult{
		DocumentRevision: "revision-b", Changes: []core.Change{
			{Operation: 0, Kind: core.OperationInsertBlock},
			{Operation: 1, Kind: core.OperationAttachFile},
		},
	}}
	tools := mustFileBlockTools(t, application, &recordingFileBlockManagement{})
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentFileAdd, toolArguments(t, `{
		"document_type":"post","document_id":"`+fileBlockTestDocumentID+`","locale":"ko",
		"expected_document_revision":"revision-a","after_block_id":"33333333-3333-4333-8333-333333333333",
		"file_id":"`+fileBlockTestFileID+`"
	}`))
	if err != nil || result.IsError {
		t.Fatalf("document_file_add = %+v, %v", result, err)
	}
	created := stringValue(t, result.StructuredContent, "block_id")
	want := []core.Operation{
		core.InsertBlockOperation(core.BlockID(created), "file", "", "33333333-3333-4333-8333-333333333333"),
		core.AttachFileOperation(core.BlockID(created), "attachment", fileBlockTestFileID),
	}
	if !reflect.DeepEqual(application.applyRequest.Operations, want) {
		t.Fatalf("operations = %+v, want %+v", application.applyRequest.Operations, want)
	}
	if application.applyRequest.ExpectedDocumentRevision != "revision-a" || application.applyRequest.Document != fileBlockTestDocumentID {
		t.Fatalf("apply CAS identity = %+v", application.applyRequest)
	}
}

func TestDocumentFileReplaceAndRemoveRequireExactFileBlock(t *testing.T) {
	t.Run("replace", func(t *testing.T) {
		application := fileBlockDocumentApplication(core.Node{ID: fileBlockTestBlockID, Kind: "file"})
		application.applyResult = core.ApplyResult{DocumentRevision: "revision-b", Changes: []core.Change{{Operation: 0, Kind: core.OperationAttachFile}}}
		tools := mustFileBlockTools(t, application, &recordingFileBlockManagement{})
		result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentFileReplace, fileBlockMutationArguments(t, `,"file_id":"`+fileBlockReplacementID+`"`))
		if err != nil || result.IsError {
			t.Fatalf("document_file_replace = %+v, %v", result, err)
		}
		wantRead := core.ReadRequest{Document: core.DocumentIdentity{Domain: core.DomainPost, Reference: fileBlockTestDocumentID}, Locale: "ko", Mode: core.ReadBlocks, Blocks: []core.BlockID{fileBlockTestBlockID}, Limit: 1}
		if !reflect.DeepEqual(application.readRequest, wantRead) {
			t.Fatalf("read request = %+v, want %+v", application.readRequest, wantRead)
		}
		want := []core.Operation{core.AttachFileOperation(fileBlockTestBlockID, "attachment", fileBlockReplacementID)}
		if !reflect.DeepEqual(application.applyRequest.Operations, want) {
			t.Fatalf("replace operations = %+v, want %+v", application.applyRequest.Operations, want)
		}
	})

	t.Run("remove", func(t *testing.T) {
		application := fileBlockDocumentApplication(core.Node{ID: fileBlockTestBlockID, Kind: "file"})
		application.applyResult = core.ApplyResult{DocumentRevision: "revision-b", Changes: []core.Change{{Operation: 0, Kind: core.OperationDeleteBlock}}}
		tools := mustFileBlockTools(t, application, &recordingFileBlockManagement{})
		result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentFileRemove, fileBlockMutationArguments(t, ""))
		if err != nil || result.IsError {
			t.Fatalf("document_file_remove = %+v, %v", result, err)
		}
		want := []core.Operation{core.DeleteBlockOperation(fileBlockTestBlockID)}
		if !reflect.DeepEqual(application.applyRequest.Operations, want) {
			t.Fatalf("remove operations = %+v, want %+v", application.applyRequest.Operations, want)
		}
	})

	for _, test := range []struct {
		name string
		node core.Node
		want string
	}{
		{name: "wrong kind", node: core.Node{ID: fileBlockTestBlockID, Kind: "paragraph"}, want: "not a File Block"},
		{name: "wrong block", node: core.Node{ID: "99999999-9999-4999-8999-999999999999", Kind: "file"}, want: "was not found"},
	} {
		t.Run(test.name, func(t *testing.T) {
			application := fileBlockDocumentApplication(test.node)
			tools := mustFileBlockTools(t, application, &recordingFileBlockManagement{})
			_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentFileRemove, fileBlockMutationArguments(t, ""))
			var executionErr *mcpserver.ToolExecutionError
			if !errors.As(err, &executionErr) || !strings.Contains(executionErr.Message, test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if len(application.applyRequest.Operations) != 0 {
				t.Fatalf("wrong Block was mutated: %+v", application.applyRequest)
			}
		})
	}
}

func TestDocumentFileReplaceReturnsStructuredDocumentRevisionConflict(t *testing.T) {
	application := fileBlockDocumentApplication(core.Node{ID: fileBlockTestBlockID, Kind: "file"})
	application.applyError = &core.ConflictError{Conflict: core.Conflict{
		Code: core.ConflictDocumentRevision, CurrentDocumentRevision: "revision-current",
		AffectedHandles: []string{"field:" + fileBlockTestBlockID + "/attachment"},
	}}
	tools := mustFileBlockTools(t, application, &recordingFileBlockManagement{})
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentFileReplace, fileBlockMutationArguments(t, `,"file_id":"`+fileBlockReplacementID+`"`))
	if err != nil || !result.IsError {
		t.Fatalf("revision conflict = %+v, %v", result, err)
	}
	conflict := result.StructuredContent["x"].([]any)
	if conflict[0] != string(core.ConflictDocumentRevision) || conflict[1] != "revision-current" {
		t.Fatalf("structured conflict = %+v", result.StructuredContent)
	}
	if application.applyRequest.ExpectedDocumentRevision != "revision-a" {
		t.Fatalf("replace lost expected revision: %+v", application.applyRequest)
	}
}

func TestDocumentFileToolsRejectUnsupportedDocumentTypeBeforeReadOrMutation(t *testing.T) {
	application := &recordingAIDocumentApplication{}
	tools := mustFileBlockTools(t, application, &recordingFileBlockManagement{})
	_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentFileRemove, toolArguments(t, `{
		"document_type":"release","document_id":"`+fileBlockTestDocumentID+`","locale":"ko",
		"expected_document_revision":"revision-a","block_id":"`+fileBlockTestBlockID+`"
	}`))
	var executionErr *mcpserver.ToolExecutionError
	if !errors.As(err, &executionErr) || !strings.Contains(executionErr.Message, "unsupported document_type") {
		t.Fatalf("unsupported document type error = %v", err)
	}
	if application.readRequest.Document.Reference != "" || len(application.applyRequest.Operations) != 0 {
		t.Fatalf("unsupported type reached applications: read=%+v apply=%+v", application.readRequest, application.applyRequest)
	}
}

func TestDocumentFileDownloadPolicyUsesServerResolvedRelationAndFileCAS(t *testing.T) {
	manager := &recordingFileBlockManagement{policy: testFileBlockPolicy(managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_PUBLIC)}
	tools := mustFileBlockTools(t, &recordingAIDocumentApplication{}, manager)

	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentFileDownloadPolicyGet, toolArguments(t, `{
		"document_type":"post","document_id":"`+fileBlockTestDocumentID+`","block_id":"`+fileBlockTestBlockID+`"
	}`))
	if err != nil || result.IsError {
		t.Fatalf("policy get = %+v, %v", result, err)
	}
	if manager.getRequest.Msg.BlockId == nil || *manager.getRequest.Msg.BlockId != fileBlockTestBlockID ||
		manager.getRequest.Msg.ReferencePath == nil || *manager.getRequest.Msg.ReferencePath != "file" {
		t.Fatalf("policy selector = %+v", manager.getRequest.Msg)
	}
	if result.StructuredContent["file_id"] != fileBlockTestFileID || result.StructuredContent["audience"] != "public" {
		t.Fatalf("policy result = %+v", result.StructuredContent)
	}

	manager.policy = testFileBlockPolicy(managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_RESTRICTED)
	manager.policy.AudienceSegments = []*managev1.AudienceSegmentSummary{{Id: fileBlockSegmentID, Name: "Members"}}
	result, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentFileDownloadPolicyUpdate, toolArguments(t, `{
		"document_type":"post","document_id":"`+fileBlockTestDocumentID+`","block_id":"`+fileBlockTestBlockID+`",
		"expected_file_id":"`+fileBlockTestFileID+`","audience":"restricted","audience_segment_ids":["`+fileBlockSegmentID+`"]
	}`))
	if err != nil || result.IsError {
		t.Fatalf("policy update = %+v, %v", result, err)
	}
	request := manager.updateRequest.Msg
	if request.ExpectedFileId != fileBlockTestFileID || request.BlockId == nil || *request.BlockId != fileBlockTestBlockID ||
		request.ReferencePath == nil || *request.ReferencePath != "file" ||
		request.Audience != managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_RESTRICTED ||
		!reflect.DeepEqual(request.AudienceSegmentIds, []string{fileBlockSegmentID}) {
		t.Fatalf("policy update request = %+v", request)
	}
	if result.StructuredContent["audience"] != "restricted" {
		t.Fatalf("policy update result = %+v", result.StructuredContent)
	}
}

func TestDocumentFileDownloadPolicyRejectsInvalidAudienceShapeBeforeCall(t *testing.T) {
	manager := &recordingFileBlockManagement{}
	tools := mustFileBlockTools(t, &recordingAIDocumentApplication{}, manager)
	for _, tail := range []string{
		`"audience":"public","audience_segment_ids":["` + fileBlockSegmentID + `"]`,
	} {
		_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentFileDownloadPolicyUpdate, toolArguments(t, `{
			"document_type":"post","document_id":"`+fileBlockTestDocumentID+`","block_id":"`+fileBlockTestBlockID+`",
			"expected_file_id":"`+fileBlockTestFileID+`",`+tail+`
		}`))
		var executionErr *mcpserver.ToolExecutionError
		if !errors.As(err, &executionErr) {
			t.Fatalf("error = %v, want ToolExecutionError", err)
		}
	}
	if manager.updateRequest != nil {
		t.Fatalf("invalid policy reached service: %+v", manager.updateRequest.Msg)
	}
}

func TestDocumentFileDownloadPolicyReturnsRestrictedEmptyFailClosedState(t *testing.T) {
	manager := &recordingFileBlockManagement{policy: testFileBlockPolicy(managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_RESTRICTED)}
	tools := mustFileBlockTools(t, &recordingAIDocumentApplication{}, manager)
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentFileDownloadPolicyGet, toolArguments(t, `{
		"document_type":"post","document_id":"`+fileBlockTestDocumentID+`","block_id":"`+fileBlockTestBlockID+`"
	}`))
	if err != nil || result.IsError {
		t.Fatalf("restricted-empty policy get = %+v, %v", result, err)
	}
	if result.StructuredContent["audience"] != "restricted" || len(result.StructuredContent["audience_segments"].([]any)) != 0 {
		t.Fatalf("restricted-empty result = %+v", result.StructuredContent)
	}
	result, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentFileDownloadPolicyUpdate, toolArguments(t, `{
		"document_type":"post","document_id":"`+fileBlockTestDocumentID+`","block_id":"`+fileBlockTestBlockID+`",
		"expected_file_id":"`+fileBlockTestFileID+`","audience":"restricted"
	}`))
	if err != nil || result.IsError {
		t.Fatalf("restricted-empty policy update = %+v, %v", result, err)
	}
	if manager.updateRequest == nil || manager.updateRequest.Msg.Audience != managev1.FileDownloadAudience_FILE_DOWNLOAD_AUDIENCE_RESTRICTED || len(manager.updateRequest.Msg.AudienceSegmentIds) != 0 {
		t.Fatalf("restricted-empty update request = %+v", manager.updateRequest)
	}
}

func TestFileUsageListReturnsExactPaginatedUsages(t *testing.T) {
	next := "next-a"
	title := "Post title"
	blockType := "file"
	link := "/admin/posts/" + fileBlockTestDocumentID
	manager := &recordingFileBlockManagement{usages: &managev1.ListFileUsagesResponse{
		Usages: []*managev1.FileUsage{{
			Domain: managev1.FileUsageDomain_FILE_USAGE_DOMAIN_POST, EntityId: fileBlockTestDocumentID,
			ReferencePath: "file", BlockId: stringPointerForFileBlockTest(fileBlockTestBlockID), Count: 1,
			BlockType: &blockType, Title: &title, Link: &link,
		}},
		NextPageToken: &next, Total: 2,
	}}
	tools := mustFileBlockTools(t, &recordingAIDocumentApplication{}, manager)
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileUsageList, toolArguments(t, `{
		"file_id":"`+fileBlockTestFileID+`","page_size":1,"page_token":"previous"
	}`))
	if err != nil || result.IsError {
		t.Fatalf("file_usage_list = %+v, %v", result, err)
	}
	if manager.usageRequest.Msg.FileId != fileBlockTestFileID || manager.usageRequest.Msg.PageSize != 1 || manager.usageRequest.Msg.GetPageToken() != "previous" {
		t.Fatalf("usage request = %+v", manager.usageRequest.Msg)
	}
	items := result.StructuredContent["usages"].([]any)
	usage := items[0].(map[string]any)
	if usage["domain"] != "post" || usage["block_id"] != fileBlockTestBlockID || result.StructuredContent["next_page_token"] != next {
		t.Fatalf("usage result = %+v", result.StructuredContent)
	}
}

func TestNewFileBlockToolsRejectsNilDependencies(t *testing.T) {
	if _, err := NewFileBlockTools(nil, &recordingFileBlockManagement{}); err == nil {
		t.Fatal("nil document application succeeded")
	}
	if _, err := NewFileBlockTools(&recordingAIDocumentApplication{}, nil); err == nil {
		t.Fatal("nil File management application succeeded")
	}
}

func mustFileBlockTools(t *testing.T, documents AIDocumentApplication, files FileBlockManagement) *FileBlockTools {
	t.Helper()
	tools, err := NewFileBlockTools(documents, files)
	if err != nil {
		t.Fatal(err)
	}
	return tools
}

func fileBlockDocumentApplication(node core.Node) *recordingAIDocumentApplication {
	return &recordingAIDocumentApplication{readResult: core.Projection{Nodes: []core.Node{node}}}
}

func fileBlockMutationArguments(t *testing.T, suffix string) mcpserver.ToolArguments {
	t.Helper()
	return toolArguments(t, `{
		"document_type":"post","document_id":"`+fileBlockTestDocumentID+`","locale":"ko",
		"expected_document_revision":"revision-a","block_id":"`+fileBlockTestBlockID+`"`+suffix+`
	}`)
}

func testFileBlockPolicy(audience managev1.FileDownloadAudience) *managev1.FileDownloadPolicy {
	blockID := fileBlockTestBlockID
	referencePath := "file"
	return &managev1.FileDownloadPolicy{
		EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		EntityId:   fileBlockTestDocumentID, BlockId: &blockID, ReferencePath: &referencePath,
		FileId: fileBlockTestFileID, Audience: audience,
	}
}

func stringPointerForFileBlockTest(value string) *string { return &value }

type recordingFileBlockManagement struct {
	getRequest    *connect.Request[managev1.GetFileDownloadPolicyRequest]
	updateRequest *connect.Request[managev1.UpdateFileDownloadPolicyRequest]
	usageRequest  *connect.Request[managev1.ListFileUsagesRequest]
	policy        *managev1.FileDownloadPolicy
	usages        *managev1.ListFileUsagesResponse
	err           error
}

func (manager *recordingFileBlockManagement) GetFileDownloadPolicy(_ context.Context, request *connect.Request[managev1.GetFileDownloadPolicyRequest]) (*connect.Response[managev1.GetFileDownloadPolicyResponse], error) {
	manager.getRequest = request
	return connect.NewResponse(&managev1.GetFileDownloadPolicyResponse{Policy: manager.policy}), manager.err
}

func (manager *recordingFileBlockManagement) UpdateFileDownloadPolicy(_ context.Context, request *connect.Request[managev1.UpdateFileDownloadPolicyRequest]) (*connect.Response[managev1.UpdateFileDownloadPolicyResponse], error) {
	manager.updateRequest = request
	return connect.NewResponse(&managev1.UpdateFileDownloadPolicyResponse{Policy: manager.policy}), manager.err
}

func (manager *recordingFileBlockManagement) ListFileUsages(_ context.Context, request *connect.Request[managev1.ListFileUsagesRequest]) (*connect.Response[managev1.ListFileUsagesResponse], error) {
	manager.usageRequest = request
	if manager.usages == nil {
		manager.usages = &managev1.ListFileUsagesResponse{}
	}
	return connect.NewResponse(manager.usages), manager.err
}
