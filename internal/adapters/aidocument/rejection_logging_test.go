package aidocumentadapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/telemetry"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

// captureProductionLogs routes the default logger through the production
// normalizer, which drops values it cannot encode safely, so a test sees the
// same fields Loki receives.
func captureProductionLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(telemetry.NewNormalizingHandler(slog.NewJSONHandler(&logs, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &logs
}

func decodeLogEntry(t *testing.T, logs *bytes.Buffer) map[string]any {
	t.Helper()
	var entry map[string]any
	require.NoError(t, json.Unmarshal(logs.Bytes(), &entry), logs.String())
	return entry
}

func TestMutationRejectionLogsBoundedIssueIdentities(t *testing.T) {
	logs := captureProductionLogs(t)
	issues := make([]core.OperationIssue, 25)
	for index := range issues {
		issues[index] = core.OperationIssue{Operation: index, Code: core.IssueInvalidBlockRelation, Handle: "block:heading", Message: "private document text"}
	}
	service, err := NewService(&fakeApplication{applyErr: &core.ValidationError{Result: core.ValidationResult{Issues: issues}}})
	require.NoError(t, err)
	mutation := mutationWithOperations(operationToProto(core.SetFieldOperation("heading", "content", core.RichText(core.InlineText("private input text")))))
	_, err = service.ApplyAIDocumentOperations(t.Context(), connect.NewRequest(&managev1.ApplyAIDocumentOperationsRequest{Mutation: mutation}))
	require.NoError(t, err)

	require.NotContains(t, logs.String(), "private")
	entry := decodeLogEntry(t, logs)
	require.Equal(t, "AI document mutation rejected", entry["msg"])
	require.EqualValues(t, 25, entry["issue_count"])
	require.Equal(t, "op0:invalid_block_relation@block:heading; op1:invalid_block_relation@block:heading; op2:invalid_block_relation@block:heading; op3:invalid_block_relation@block:heading; op4:invalid_block_relation@block:heading; +20 more", entry["issues"])
	require.Equal(t, "post-stable", entry["document_id"])
	require.Equal(t, "en", entry["locale"])
	require.Equal(t, "post", entry["domain"])
}

func TestExactMutationRejectionLogsDocumentAndIssues(t *testing.T) {
	logs := captureProductionLogs(t)
	rejected := rejectExactMutation("Page", core.ValidationResult{Issues: []core.OperationIssue{{
		Operation: 1, Code: core.IssueInvalidBlockRelation, Handle: "block-7", Message: "private document text",
	}}})
	request := core.ApplyRequest{Profile: core.DomainPage, Document: "page-stable", Locale: "ko"}

	_, err, handled := handleExactMutationValidationError(t.Context(), request, rejected, false)
	require.True(t, handled)
	require.Error(t, err)

	require.NotContains(t, logs.String(), "private")
	entry := decodeLogEntry(t, logs)
	require.Equal(t, "AI document validation rejected operations", entry["msg"])
	require.Equal(t, "Page AI document operations are invalid", entry["failure_cause"])
	require.Equal(t, "Page", entry["domain"])
	require.Equal(t, "page-stable", entry["document_id"])
	require.Equal(t, "ko", entry["locale"])
	require.Equal(t, false, entry["validate_only"])
	require.EqualValues(t, 1, entry["issue_count"])
	require.Equal(t, "op1:invalid_block_relation@block-7", entry["issues"])
}

func TestInteractiveRelayFailureLogsJoinableRelayIdentity(t *testing.T) {
	logs := captureProductionLogs(t)
	collabErr := connect.NewWireError(connect.CodeInternal, errors.New("internal error"))
	decorated, _, _ := interactiveApplicationFixture(
		t,
		interactiveChangedSourceResult(),
		collabErr,
		&interactiveFallbackPublisher{},
		intrav1.InteractiveAIDocumentMutationOrigin_INTERACTIVE_AI_DOCUMENT_MUTATION_ORIGIN_MCP,
	)
	request := interactiveSourceRequest(core.DomainPage)
	request.Operations = []core.Operation{core.SetFieldOperation("block-1", "content", core.Text("private document text"))}

	_, err := decorated.Apply(interactiveMemberContext(), request)
	require.NoError(t, err)

	require.NotContains(t, logs.String(), "private")
	require.NotContains(t, logs.String(), "22222222-2222-4222-8222-222222222222", "actor member id is denylisted")
	entry := decodeLogEntry(t, logs)
	require.Equal(t, "Interactive AI document relay failed; transactional content event retained", entry["msg"])
	require.Equal(t, "relay committed interactive AI document mutation: internal: internal error", entry["failure_cause"])
	require.Equal(t, "editor_collab", entry["dependency"])
	require.Equal(t, "rpc", entry["relay_stage"])
	require.Equal(t, "internal", entry["connect_code"])
	require.Equal(t, true, entry["wire_error"])
	require.Equal(t, "019cd2ba-aabe-7fec-ac92-aef81b16565b", entry["mutation_id"])
	require.Equal(t, "mcp", entry["origin"])
	require.Equal(t, "11111111-1111-4111-8111-111111111111", entry["document_id"])
	require.Equal(t, "ko", entry["locale"])
	require.Equal(t, "page", entry["domain"])
	require.EqualValues(t, 1, entry["operation_count"])
	require.Equal(t, "delete_block", entry["operation_kinds"])
}
