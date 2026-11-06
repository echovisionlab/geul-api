package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
)

// documentBatchMaxDocuments bounds the documents one call acts on.
const documentBatchMaxDocuments = 50

// What became of one document in a call over many.
const (
	documentStatusApplied    = "applied"
	documentStatusWouldApply = "would_apply"
	documentStatusFailed     = "failed"
)

// documentOutcome is what a call over many documents reports about each one.
// Each document stands alone: one that fails leaves the others done.
type documentOutcome struct {
	DocumentType core.Domain            `json:"document_type"`
	DocumentID   core.DocumentReference `json:"document_id"`
	Status       string                 `json:"status"`
	Error        string                 `json:"error,omitempty"`
}

func (outcome documentOutcome) failed() bool { return outcome.Status == documentStatusFailed }

// fail records err on the outcome: its message when a caller can act on it, and
// otherwise a log entry and a general message.
func (outcome *documentOutcome) fail(ctx context.Context, err error) {
	outcome.Status = documentStatusFailed
	if execution, ok := errors.AsType[*mcpserver.ToolExecutionError](err); ok && execution != nil {
		outcome.Error = execution.Message
		return
	}
	slog.ErrorContext(ctx, "MCP document batch left a document unchanged", failurecause.Attr(err), "document_id", string(outcome.DocumentID), "domain", string(outcome.DocumentType))
	outcome.Error = "the operation failed on the server; nothing changed in this document"
}

// encodeDocumentBatch is an error only when no document applied, so a caller
// never reads a partly applied call as a failure of all of it.
func encodeDocumentBatch[Result interface{ failed() bool }](results []Result) (mcpserver.ToolResult, error) {
	failed := 0
	for _, result := range results {
		if result.failed() {
			failed++
		}
	}
	encoded, err := json.Marshal(struct {
		Documents []Result `json:"documents"`
		Applied   int      `json:"applied"`
		Failed    int      `json:"failed"`
	}{results, len(results) - failed, failed})
	if err != nil {
		return mcpserver.ToolResult{}, fmt.Errorf("encode MCP document batch: %w", err)
	}
	return structuredResult(encoded, failed == len(results))
}
