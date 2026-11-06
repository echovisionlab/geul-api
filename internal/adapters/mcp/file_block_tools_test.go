package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	filemediadomain "github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mediaasset"

	"connectrpc.com/connect"
	core "github.com/echovisionlab/geul-api/internal/aidocument"
	fileauthority "github.com/echovisionlab/geul-api/internal/filemedia"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const (
	fileBlockTestDocumentID = "44444444-4444-4444-8444-444444444444"
	fileBlockTestBlockID    = "55555555-5555-4555-8555-555555555555"
	fileBlockTestFileID     = "66666666-6666-4666-8666-666666666666"
	fileBlockReplacementID  = "77777777-7777-4777-8777-777777777777"
	fileBlockCriteriaID     = "88888888-8888-4888-8888-888888888888"
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
		{ToolDocumentFileCaptionUpdate, toolAnnotations(false, true, false)},
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
	getSchema := string(listed[4].InputSchema)
	if strings.Contains(getSchema, "reference_path") || strings.Contains(getSchema, "file_id") {
		t.Fatalf("policy get schema lets callers assert relation authority: %s", getSchema)
	}
	updateSchema := string(listed[5].InputSchema)
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

func TestDocumentFileAddIncludesOptionalCaptionInAtomicBatch(t *testing.T) {
	for _, caption := range []string{"image caption", ""} {
		t.Run(caption, func(t *testing.T) {
			application := &recordingAIDocumentApplication{applyResult: core.ApplyResult{DocumentRevision: "revision-b"}}
			tools := mustFileBlockTools(t, application, &recordingFileBlockManagement{})
			encodedCaption, err := json.Marshal(caption)
			if err != nil {
				t.Fatal(err)
			}
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentFileAdd, toolArguments(t, `{
				"document_type":"page","document_id":"`+fileBlockTestDocumentID+`","locale":"en",
				"expected_document_revision":"revision-a","parent_block_id":"page-rich-text-section",
				"file_id":"`+fileBlockTestFileID+`","caption":`+string(encodedCaption)+`
			}`))
			if err != nil {
				t.Fatal(err)
			}
			created := core.BlockID(stringValue(t, result.StructuredContent, "block_id"))
			want := []core.Operation{
				core.InsertBlockOperation(created, "file", "page-rich-text-section", ""),
				core.AttachFileOperation(created, "attachment", fileBlockTestFileID),
				core.SetFieldOperation(created, "caption", core.Text(caption)),
			}
			if !reflect.DeepEqual(application.applyRequest.Operations, want) {
				t.Fatalf("operations = %+v, want %+v", application.applyRequest.Operations, want)
			}
		})
	}
}

func TestDocumentFileCaptionUpdateUsesLocaleFieldAndExactRevisions(t *testing.T) {
	for _, caption := range []string{"translated caption", ""} {
		t.Run(caption, func(t *testing.T) {
			application := fileBlockDocumentApplication(core.Node{ID: fileBlockTestBlockID, Kind: "file"})
			application.applyResult = core.ApplyResult{DocumentRevision: "revision-a", TargetRevision: revisionPointer("target-b"), Changes: []core.Change{{Operation: 0, Kind: core.OperationSetField}}}
			tools := mustFileBlockTools(t, application, &recordingFileBlockManagement{})
			encoded, _ := json.Marshal(caption)
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentFileCaptionUpdate, toolArguments(t, `{
				"document_type":"page","document_id":"`+fileBlockTestDocumentID+`","locale":"ko",
				"expected_document_revision":"revision-a","expected_target_revision":"target-a",
				"block_id":"`+fileBlockTestBlockID+`","caption":`+string(encoded)+`
			}`))
			if err != nil || result.IsError {
				t.Fatalf("caption update=%+v,%v", result, err)
			}
			wantRead := core.ReadRequest{Document: core.DocumentIdentity{Domain: core.DomainPage, Reference: fileBlockTestDocumentID}, Locale: "ko", Mode: core.ReadBlocks, Blocks: []core.BlockID{fileBlockTestBlockID}, Limit: 1}
			if !reflect.DeepEqual(application.readRequest, wantRead) {
				t.Fatalf("read=%+v,want %+v", application.readRequest, wantRead)
			}
			want := []core.Operation{core.SetFieldOperation(fileBlockTestBlockID, "caption", core.Text(caption))}
			if !reflect.DeepEqual(application.applyRequest.Operations, want) {
				t.Fatalf("operations=%+v,want %+v", application.applyRequest.Operations, want)
			}
			if application.applyRequest.ExpectedDocumentRevision != "revision-a" || application.applyRequest.ExpectedTargetRevision == nil || *application.applyRequest.ExpectedTargetRevision != "target-a" {
				t.Fatalf("revisions=%+v", application.applyRequest)
			}
			if got := stringValue(t, result.StructuredContent, "target_revision"); got != "target-b" {
				t.Fatalf("target revision=%q", got)
			}
		})
	}
}

func TestDocumentFileCaptionRejectsInvalidInputAndWrongBlock(t *testing.T) {
	for _, test := range []struct {
		name, extra string
		node        core.Node
	}{
		{ToolDocumentFileCaptionUpdate, "", core.Node{ID: fileBlockTestBlockID, Kind: "file"}},
		{ToolDocumentFileCaptionUpdate, `,"caption":null`, core.Node{ID: fileBlockTestBlockID, Kind: "file"}},
		{ToolDocumentFileCaptionUpdate, `,"caption":[]`, core.Node{ID: fileBlockTestBlockID, Kind: "file"}},
		{ToolDocumentFileCaptionUpdate, `,"caption":"test"`, core.Node{ID: fileBlockTestBlockID, Kind: "paragraph"}},
		{ToolDocumentFileCaptionUpdate, `,"caption":"test"`, core.Node{ID: "different", Kind: "file"}},
		{ToolDocumentFileAdd, `,"file_id":"` + fileBlockTestFileID + `","caption":null`, core.Node{}},
	} {
		t.Run(test.name+test.extra+string(test.node.Kind), func(t *testing.T) {
			application := fileBlockDocumentApplication(test.node)
			tools := mustFileBlockTools(t, application, &recordingFileBlockManagement{})
			args := fileBlockMutationArguments(t, test.extra)
			if test.name == ToolDocumentFileAdd {
				delete(args, "block_id")
			}
			_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, test.name, args)
			var execution *mcpserver.ToolExecutionError
			if !errors.As(err, &execution) {
				t.Fatalf("error=%v,want execution error", err)
			}
			if len(application.applyRequest.Operations) != 0 {
				t.Fatalf("invalid caption reached mutation: %+v", application.applyRequest)
			}
		})
	}
}

func TestDocumentFileCaptionPreservesApplicationConflictAndDenial(t *testing.T) {
	application := fileBlockDocumentApplication(core.Node{ID: fileBlockTestBlockID, Kind: "file"})
	application.applyError = &core.ConflictError{Conflict: core.Conflict{Code: core.ConflictTargetRevision, CurrentDocumentRevision: "revision-a", CurrentTargetRevision: revisionPointer("target-current"), AffectedHandles: []string{"field:" + fileBlockTestBlockID + "/caption"}}}
	tools := mustFileBlockTools(t, application, &recordingFileBlockManagement{})
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentFileCaptionUpdate, fileBlockMutationArguments(t, `,"caption":"new"`))
	if err != nil || result.IsError {
		t.Fatalf("caption conflict=%+v,%v", result, err)
	}
	assertDocumentSync(t, result, "target_revision_changed", "revision-a", revisionPointer("target-current"), []string{"field:" + fileBlockTestBlockID + "/caption"}, false, map[string]any{"document_type": "post", "document_id": fileBlockTestDocumentID, "locale": "ko", "mode": "blocks", "block_ids": []any{fileBlockTestBlockID}, "limit": float64(1)})
	application.applyError = connect.NewError(connect.CodePermissionDenied, errors.New("caption edit denied"))
	_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentFileCaptionUpdate, fileBlockMutationArguments(t, `,"caption":"new"`))
	var execution *mcpserver.ToolExecutionError
	if !errors.As(err, &execution) || !strings.Contains(execution.Message, "caption edit denied") {
		t.Fatalf("denial=%v", err)
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
	if err != nil || result.IsError {
		t.Fatalf("revision conflict = %+v, %v", result, err)
	}
	assertDocumentSync(t, result, "document_revision_changed", "revision-current", nil, []string{"field:" + fileBlockTestBlockID + "/attachment"}, false, map[string]any{"document_type": "post", "document_id": fileBlockTestDocumentID, "locale": "ko", "mode": "blocks", "block_ids": []any{fileBlockTestBlockID}, "limit": float64(1)})
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
	manager := &recordingFileBlockManagement{policy: testFileBlockPolicy(managev1.FileDownloadAccess_FILE_DOWNLOAD_ACCESS_PUBLIC)}
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
	if result.StructuredContent["file_id"] != fileBlockTestFileID || result.StructuredContent["access"] != "public" {
		t.Fatalf("policy result = %+v", result.StructuredContent)
	}

	manager.policy = testFileBlockPolicy(managev1.FileDownloadAccess_FILE_DOWNLOAD_ACCESS_RESTRICTED)
	manager.policy.MemberCriteria = []*filemediadomain.PolicyCriteria{{ID: fileBlockCriteriaID, Name: "Members"}}
	result, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentFileDownloadPolicyUpdate, toolArguments(t, `{
		"document_type":"post","document_id":"`+fileBlockTestDocumentID+`","block_id":"`+fileBlockTestBlockID+`",
		"expected_file_id":"`+fileBlockTestFileID+`","access":"restricted","member_criteria_ids":["`+fileBlockCriteriaID+`"],
		"observed_policy":{"access":"public","member_criteria_ids":[]}
	}`))
	if err != nil || result.IsError {
		t.Fatalf("policy update = %+v, %v", result, err)
	}
	request := manager.updateRequest.Msg
	if request.ExpectedFileId != fileBlockTestFileID || request.BlockId == nil || *request.BlockId != fileBlockTestBlockID ||
		request.ReferencePath == nil || *request.ReferencePath != "file" ||
		request.Access != managev1.FileDownloadAccess_FILE_DOWNLOAD_ACCESS_RESTRICTED ||
		!reflect.DeepEqual(request.MemberCriteriaIds, []string{fileBlockCriteriaID}) ||
		request.ObservedPolicy == nil || request.ObservedPolicy.Access != managev1.FileDownloadAccess_FILE_DOWNLOAD_ACCESS_PUBLIC ||
		len(request.ObservedPolicy.MemberCriteriaIds) != 0 {
		t.Fatalf("policy update request = %+v", request)
	}
	if result.StructuredContent["access"] != "restricted" {
		t.Fatalf("policy update result = %+v", result.StructuredContent)
	}
}

func TestDocumentFileDownloadPolicyRejectsInvalidAccessShapeBeforeCall(t *testing.T) {
	manager := &recordingFileBlockManagement{}
	tools := mustFileBlockTools(t, &recordingAIDocumentApplication{}, manager)
	for _, tail := range []string{
		`"access":"public","member_criteria_ids":["` + fileBlockCriteriaID + `"]`,
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
	manager := &recordingFileBlockManagement{policy: testFileBlockPolicy(managev1.FileDownloadAccess_FILE_DOWNLOAD_ACCESS_RESTRICTED)}
	tools := mustFileBlockTools(t, &recordingAIDocumentApplication{}, manager)
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentFileDownloadPolicyGet, toolArguments(t, `{
		"document_type":"post","document_id":"`+fileBlockTestDocumentID+`","block_id":"`+fileBlockTestBlockID+`"
	}`))
	if err != nil || result.IsError {
		t.Fatalf("restricted-empty policy get = %+v, %v", result, err)
	}
	if result.StructuredContent["access"] != "restricted" || len(result.StructuredContent["member_criteria"].([]any)) != 0 {
		t.Fatalf("restricted-empty result = %+v", result.StructuredContent)
	}
	result, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentFileDownloadPolicyUpdate, toolArguments(t, `{
		"document_type":"post","document_id":"`+fileBlockTestDocumentID+`","block_id":"`+fileBlockTestBlockID+`",
		"expected_file_id":"`+fileBlockTestFileID+`","access":"restricted",
		"observed_policy":{"access":"restricted","member_criteria_ids":[]}
	}`))
	if err != nil || result.IsError {
		t.Fatalf("restricted-empty policy update = %+v, %v", result, err)
	}
	if manager.updateRequest == nil || manager.updateRequest.Msg.Access != managev1.FileDownloadAccess_FILE_DOWNLOAD_ACCESS_RESTRICTED || len(manager.updateRequest.Msg.MemberCriteriaIds) != 0 {
		t.Fatalf("restricted-empty update request = %+v", manager.updateRequest)
	}
}

func TestDocumentFileDownloadPolicyPreservesExactObservedBaseline(t *testing.T) {
	manager := &recordingFileBlockManagement{policy: testFileBlockPolicy(managev1.FileDownloadAccess_FILE_DOWNLOAD_ACCESS_PUBLIC)}
	tools := mustFileBlockTools(t, &recordingAIDocumentApplication{}, manager)
	_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentFileDownloadPolicyUpdate, toolArguments(t, `{
		"document_type":"post","document_id":"`+fileBlockTestDocumentID+`","block_id":"`+fileBlockTestBlockID+`",
		"expected_file_id":"`+fileBlockTestFileID+`","access":"public",
		"observed_policy":{"access":"restricted","member_criteria_ids":["`+fileBlockCriteriaID+`"]}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	want := &managev1.FileDownloadPolicyObservedState{
		Access:            managev1.FileDownloadAccess_FILE_DOWNLOAD_ACCESS_RESTRICTED,
		MemberCriteriaIds: []string{fileBlockCriteriaID},
	}
	if !reflect.DeepEqual(manager.updateRequest.Msg.ObservedPolicy, want) || manager.getRequest != nil {
		t.Fatalf("observed baseline was replaced or reread: request=%+v get=%+v", manager.updateRequest.Msg, manager.getRequest)
	}
}

func TestDocumentFileDownloadPolicyRejectsMissingOrInvalidObservedBaseline(t *testing.T) {
	for _, baseline := range []string{
		``,
		`,"observed_policy":null`,
		`,"observed_policy":{"access":"disabled"}`,
		`,"observed_policy":{"access":"disabled","member_criteria_ids":null}`,
		`,"observed_policy":{"access":"unknown","member_criteria_ids":[]}`,
		`,"observed_policy":{"access":"public","member_criteria_ids":["` + fileBlockCriteriaID + `"]}`,
		`,"observed_policy":{"access":"restricted","member_criteria_ids":["` + fileBlockCriteriaID + `","` + fileBlockCriteriaID + `"]}`,
		`,"observed_policy":{"access":"restricted","member_criteria_ids":["not-a-uuid"]}`,
	} {
		t.Run(baseline, func(t *testing.T) {
			manager := &recordingFileBlockManagement{}
			tools := mustFileBlockTools(t, &recordingAIDocumentApplication{}, manager)
			_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentFileDownloadPolicyUpdate, toolArguments(t, `{
				"document_type":"post","document_id":"`+fileBlockTestDocumentID+`","block_id":"`+fileBlockTestBlockID+`",
				"expected_file_id":"`+fileBlockTestFileID+`","access":"disabled"`+baseline+`
			}`))
			var executionErr *mcpserver.ToolExecutionError
			if !errors.As(err, &executionErr) || manager.updateRequest != nil {
				t.Fatalf("invalid baseline reached service: err=%v request=%+v", err, manager.updateRequest)
			}
		})
	}
}

func TestDocumentFileDownloadPolicySatisfiesRealServiceInputContract(t *testing.T) {
	tools := mustFileBlockTools(t, &recordingAIDocumentApplication{}, &fileauthority.Service{})
	_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentFileDownloadPolicyUpdate, toolArguments(t, `{
		"document_type":"post","document_id":"`+fileBlockTestDocumentID+`","block_id":"`+fileBlockTestBlockID+`",
		"expected_file_id":"`+fileBlockTestFileID+`","access":"public",
		"observed_policy":{"access":"disabled","member_criteria_ids":[]}
	}`))
	var executionErr *mcpserver.ToolExecutionError
	// The actual service validates the policy input before checking its actor.
	// Reaching authentication proves this request passed the required baseline contract.
	if !errors.As(err, &executionErr) || !strings.Contains(executionErr.Message, "unauthenticated") {
		t.Fatalf("real service did not reach actor validation: %v", err)
	}
}

func TestFileUsageListReturnsExactPaginatedUsages(t *testing.T) {
	next := "next-a"
	title := "Post title"
	blockType := "file"
	link := "/admin/posts/" + fileBlockTestDocumentID
	manager := &recordingFileBlockManagement{usages: &fileauthority.FileUsagePage{
		Usages: []*fileauthority.UsageView{{
			Domain: fileauthority.UsageDomain(managev1.FileUsageDomain_FILE_USAGE_DOMAIN_POST), EntityID: fileBlockTestDocumentID,
			ReferencePath: "file", BlockID: stringPointerForFileBlockTest(fileBlockTestBlockID), Count: 1,
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

func testFileBlockPolicy(access managev1.FileDownloadAccess) *fileauthority.DownloadPolicy {
	blockID := fileBlockTestBlockID
	referencePath := "file"
	return &fileauthority.DownloadPolicy{
		EntityType: int32(managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST),
		EntityID:   fileBlockTestDocumentID, BlockID: &blockID, ReferencePath: &referencePath,
		FileID: fileBlockTestFileID, Access: mediaasset.FileDownloadAccess(strings.ToLower(strings.TrimPrefix(access.String(), "FILE_DOWNLOAD_ACCESS_"))),
	}
}

func stringPointerForFileBlockTest(value string) *string { return &value }

type recordingFileBlockManagement struct {
	getRequest    *connect.Request[managev1.GetFileDownloadPolicyRequest]
	updateRequest *connect.Request[managev1.UpdateFileDownloadPolicyRequest]
	usageRequest  *connect.Request[managev1.ListFileUsagesRequest]
	policy        *fileauthority.DownloadPolicy
	usages        *fileauthority.FileUsagePage
	err           error
}

// The fake records each use-case call as the request a Connect client would
// have sent, so the assertions read the native field names.
func (manager *recordingFileBlockManagement) GetFileDownloadPolicy(_ context.Context, query fileauthority.GetFileDownloadPolicyQuery) (*fileauthority.DownloadPolicy, error) {
	manager.getRequest = connect.NewRequest(&managev1.GetFileDownloadPolicyRequest{
		EntityType: managev1.TranscodeEntityType(query.Target.EntityType), EntityId: query.Target.EntityID,
		BlockId: query.Target.BlockID, ReferencePath: query.Target.ReferencePath,
	})
	return manager.policy, manager.err
}

func (manager *recordingFileBlockManagement) UpdateFileDownloadPolicy(_ context.Context, command fileauthority.UpdateFileDownloadPolicyCommand) (*fileauthority.DownloadPolicy, error) {
	request := &managev1.UpdateFileDownloadPolicyRequest{
		EntityType: managev1.TranscodeEntityType(command.Target.EntityType), EntityId: command.Target.EntityID,
		BlockId: command.Target.BlockID, ReferencePath: command.Target.ReferencePath,
		ExpectedFileId: command.ExpectedFileID, Access: managev1.FileDownloadAccess(command.Policy.Access), MemberCriteriaIds: command.Policy.MemberCriteriaIDs,
	}
	if observed := command.Observed; observed != nil {
		request.ObservedPolicy = &managev1.FileDownloadPolicyObservedState{Access: managev1.FileDownloadAccess(observed.Access), MemberCriteriaIds: observed.MemberCriteriaIDs}
	}
	manager.updateRequest = connect.NewRequest(request)
	return manager.policy, manager.err
}

func (manager *recordingFileBlockManagement) ListFileUsages(_ context.Context, query fileauthority.ListFileUsagesQuery) (fileauthority.FileUsagePage, error) {
	request := &managev1.ListFileUsagesRequest{FileId: query.FileID, PageSize: query.PageSize}
	if query.PageToken != "" {
		request.PageToken = &query.PageToken
	}
	manager.usageRequest = connect.NewRequest(request)
	if manager.usages == nil {
		manager.usages = &fileauthority.FileUsagePage{}
	}
	return fileauthority.FileUsagePage{Usages: manager.usages.Usages, NextPageToken: manager.usages.NextPageToken, Total: manager.usages.Total}, manager.err
}

func TestFileUsageListStillRejectsUnknownDomainValues(t *testing.T) {
	manager := &recordingFileBlockManagement{usages: &fileauthority.FileUsagePage{Usages: []*fileauthority.UsageView{{Domain: 999, EntityID: fileBlockTestDocumentID, ReferencePath: "file", Count: 1}}}}
	tools := mustFileBlockTools(t, &recordingAIDocumentApplication{}, manager)
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFileUsageList, toolArguments(t, `{"file_id":"`+fileBlockTestFileID+`"}`))
	if err == nil || !strings.Contains(err.Error(), `unsupported domain "999"`) || result.Content != nil {
		t.Fatalf("unknown usage domain: result=%+v, error=%v", result, err)
	}
}
