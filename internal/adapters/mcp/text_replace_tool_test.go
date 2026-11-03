package mcp

import (
	"reflect"
	"testing"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
)

func TestTextReplaceFindsTheBlockAndWritesOnlyThatField(t *testing.T) {
	paragraph := core.Node{ID: "paragraph-a", Kind: "paragraph", Localized: []core.FieldValue{{ID: "content", Value: core.RichText(
		core.InlineText("Turn on "), core.Bold(core.InlineText("route recording")), core.InlineText("."),
	)}}}
	application := &recordingAIDocumentApplication{
		readResult:  core.Projection{DocumentRevision: "current", Nodes: []core.Node{paragraph}},
		applyResult: core.ApplyResult{DocumentRevision: "next", Changed: true, Changes: []core.Change{{Kind: core.OperationSetField, AffectedHandles: []string{"paragraph-a"}}}},
	}
	call := func(expected string) (mcpserver.ToolResult, error) {
		return mustAIDocumentTools(t, application).CallTool(t.Context(), mcpserver.Principal{}, ToolTextReplace, toolArguments(t,
			`{"document_type":"page","document_id":"`+syncTestDocument+`","locale":"ko","expected_document_revision":"`+expected+`","find":"route recording","replace":"path logging"}`))
	}

	result, err := call("current")
	if err != nil || result.StructuredContent["dr"] != "next" {
		t.Fatalf("text replace = %+v, %v", result, err)
	}
	operations := application.applyRequest.Operations
	want := core.RichText(core.InlineText("Turn on "), core.Bold(core.InlineText("path logging")), core.InlineText("."))
	if application.readCalls != 2 || len(operations) != 1 || !reflect.DeepEqual(operations[0].SetField.Value, want) {
		t.Fatalf("reads %d, operations %+v", application.readCalls, operations)
	}

	application.applyCalls = 0
	result, err = call("stale")
	if err != nil || result.StructuredContent["status"] != "sync_required" || application.applyCalls != 0 {
		t.Fatalf("stale text replace = %+v, %v, apply calls %d", result, err, application.applyCalls)
	}
}
