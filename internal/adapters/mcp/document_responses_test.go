package mcp

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"connectrpc.com/connect"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
)

func TestAcceptedResponseWireContract(t *testing.T) {
	targetRevision := core.Revision("target-revision")
	tests := []struct {
		name    string
		result  core.ApplyResult
		created core.BlockID
		want    string
	}{
		{
			name:   "no changes or target revision",
			result: core.ApplyResult{DocumentRevision: "document-revision"},
			want:   `{"changes":[],"document_revision":"document-revision"}`,
		},
		{
			name: "created block with target revision",
			result: core.ApplyResult{DocumentRevision: "document-revision", TargetRevision: &targetRevision, Changes: []core.Change{
				{Operation: 0, Kind: core.OperationInsertBlock, AffectedHandles: []string{"block-a", "section-a"}},
				{Operation: 1, Kind: core.OperationSetField},
			}},
			created: "block-a",
			want:    `{"block_id":"block-a","changes":[{"affected_handles":["block-a","section-a"],"op":"insert_block","operation_index":0},{"affected_handles":[],"op":"set_field","operation_index":1}],"document_revision":"document-revision","target_revision":"target-revision"}`,
		},
		{
			name: "empty handles remain arrays with a supplied target revision",
			result: core.ApplyResult{DocumentRevision: "document-revision", TargetRevision: &targetRevision, Changes: []core.Change{
				{Operation: 0, Kind: core.OperationDeleteBlock, AffectedHandles: []string{}},
			}},
			want: `{"changes":[{"affected_handles":[],"op":"delete_block","operation_index":0}],"document_revision":"document-revision","target_revision":"target-revision"}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := encodeFocusedAccepted(test.result, test.created)
			if err != nil || string(encoded) != test.want {
				t.Fatalf("accepted response = %s, %v; want %s", encoded, err, test.want)
			}
			var wantStructured map[string]any
			if err := json.Unmarshal([]byte(test.want), &wantStructured); err != nil {
				t.Fatal(err)
			}
			for _, isError := range []bool{false, true} {
				response, err := structuredResult(encoded, isError)
				if err != nil {
					t.Fatal(err)
				}
				if response.IsError != isError || len(response.Content) != 1 || response.Content[0]["text"] != test.want ||
					!reflect.DeepEqual(response.StructuredContent, wantStructured) {
					t.Fatalf("response text/structured/error parity lost: %+v", response)
				}
			}
		})
	}
}

func TestValidationResponsePreservesOperationIndexAndArrayHandles(t *testing.T) {
	validation := core.ValidationResult{Issues: []core.OperationIssue{{
		Operation: 2, Code: core.IssueInvalidOperation, Handle: "block:form:field:field-a",
		Message: "source payload identity does not match the block handle",
	}}}
	application := &recordingAIDocumentApplication{applyError: &core.ValidationError{Result: validation}}
	response, err := mustAIDocumentTools(t, application).applyRequest(t.Context(), core.ApplyRequest{}, "")
	if err != nil || !response.IsError || len(response.Content) != 1 {
		t.Fatalf("operation rejection = %+v, %v", response, err)
	}
	want := `{"issues":[{"code":"invalid_operation","handle":"block:form:field:field-a","message":"source payload identity does not match the block handle","operation_index":2}]}`
	if response.Content[0]["text"] != want {
		t.Fatalf("rejection text = %v; want %s", response.Content[0]["text"], want)
	}
	encoded, err := encodeValidation(core.ValidationResult{Conflict: &core.Conflict{
		Code: core.ConflictDocumentRevision, CurrentDocumentRevision: "current-revision",
	}})
	if err != nil || string(encoded) != `{"conflict":{"affected_handles":[],"code":"document_revision_conflict","current_document_revision":"current-revision","current_target_revision":null}}` {
		t.Fatalf("batch conflict = %s, %v", encoded, err)
	}
}

func TestBatchCompilationErrorIsAnActionableToolFailure(t *testing.T) {
	application := &recordingAIDocumentApplication{applyError: connect.NewError(connect.CodeInvalidArgument,
		errors.New("operations: incomplete required block content"))}
	tools := mustAIDocumentTools(t, application)
	config := validHTTPConfig(nil)
	config.Registry, config.Dispatcher = tools, tools
	response := httptest.NewRecorder()
	newHTTPTestHandler(t, config).ServeHTTP(response, mcpHTTPRequest(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"document_apply","arguments":{"document_type":"form","document_id":"55555555-5555-4555-8555-555555555555","locale":"en","expected_document_revision":"revision","operations":[{"op":"unset_field","block_id":"document","field":"title"}]}}}`))
	var envelope struct {
		Error  json.RawMessage `json:"error"`
		Result struct {
			IsError bool             `json:"isError"`
			Content []map[string]any `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || len(envelope.Error) != 0 || !envelope.Result.IsError ||
		len(envelope.Result.Content) != 1 || envelope.Result.Content[0]["text"] != "operations: incomplete required block content" {
		t.Fatalf("batch rejection = %d %s", response.Code, response.Body.String())
	}
}

func TestAcceptedResponseFailsClosedBeforeAddingCreatedBlock(t *testing.T) {
	tests := []struct {
		name   string
		result core.ApplyResult
		want   string
	}{
		{name: "missing revision", want: "application returned an accepted mutation without document revision"},
		{
			name:   "negative operation",
			result: core.ApplyResult{DocumentRevision: "revision", Changes: []core.Change{{Operation: -1, Kind: core.OperationInsertBlock}}},
			want:   "application returned a negative accepted operation index",
		},
		{
			name:   "unknown kind",
			result: core.ApplyResult{DocumentRevision: "revision", Changes: []core.Change{{Kind: "unsupported"}}},
			want:   `application returned unsupported accepted operation kind "unsupported"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, created := range []core.BlockID{"", "block-a"} {
				encoded, err := encodeFocusedAccepted(test.result, created)
				if encoded != nil || err == nil || err.Error() != test.want {
					t.Fatalf("accepted response (%q) = %s, %v; want no output and %s", created, encoded, err, test.want)
				}
				response, err := mustAIDocumentTools(t, &recordingAIDocumentApplication{applyResult: test.result}).applyRequest(t.Context(), core.ApplyRequest{}, created)
				if err == nil || err.Error() != "encode MCP accepted mutation: "+test.want || response.Content != nil || response.StructuredContent != nil {
					t.Fatalf("invalid application result leaked through response: %+v, %v", response, err)
				}
			}
		})
	}
}
