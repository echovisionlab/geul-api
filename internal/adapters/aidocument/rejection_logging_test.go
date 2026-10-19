package aidocumentadapter

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	core "github.com/echovisionlab/geul-api/internal/aidocument"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestMutationRejectionLogsBoundedIssueIdentities(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	issues := make([]core.OperationIssue, 25)
	for index := range issues {
		issues[index] = core.OperationIssue{Operation: index, Code: core.IssueInvalidBlockRelation, Handle: "heading", Message: "private document text"}
	}
	service, err := NewService(&fakeApplication{applyErr: &core.ValidationError{Result: core.ValidationResult{Issues: issues}}})
	require.NoError(t, err)
	mutation := mutationWithOperations(operationToProto(core.SetFieldOperation("heading", "content", core.RichText(core.InlineText("private input text")))))
	_, err = service.ApplyAIDocumentOperations(t.Context(), connect.NewRequest(&managev1.ApplyAIDocumentOperationsRequest{Mutation: mutation}))
	require.NoError(t, err)
	require.NotContains(t, logs.String(), "private")
	var entry struct {
		IssueCount int                           `json:"issue_count"`
		Issues     []core.ValidationIssueSummary `json:"issues"`
	}
	require.NoError(t, json.Unmarshal(logs.Bytes(), &entry))
	require.Equal(t, 25, entry.IssueCount)
	require.Len(t, entry.Issues, 20)
	require.Equal(t, core.ValidationIssueSummary{Operation: 19, Code: core.IssueInvalidBlockRelation, Handle: "heading"}, entry.Issues[19])
}
