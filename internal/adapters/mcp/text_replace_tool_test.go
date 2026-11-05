package mcp

import (
	"reflect"
	"strings"
	"testing"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
)

func TestTextReplaceNeedsNoRevisionsBecauseFindGuardsIt(t *testing.T) {
	target := core.Revision("target-current")
	paragraph := core.Node{ID: "paragraph-a", Kind: "paragraph", Localized: []core.FieldValue{{ID: "content", Value: core.RichText(core.InlineText("route recording"))}}}
	application := &recordingAIDocumentApplication{
		readResult:  core.Projection{DocumentRevision: "current", TargetRevision: &target, Nodes: []core.Node{paragraph}},
		applyResult: core.ApplyResult{DocumentRevision: "current", TargetRevision: revisionPointer("target-next"), Changed: true, Changes: []core.Change{{Kind: core.OperationSetField, AffectedHandles: []string{"field:paragraph-a/content"}}}},
	}

	result, err := mustAIDocumentTools(t, application).CallTool(t.Context(), mcpserver.Principal{}, ToolTextReplace, toolArguments(t,
		`{"document_type":"page","document_id":"`+syncTestDocument+`","locale":"en","find":"route recording","replace":"path logging"}`))

	if err != nil || result.IsError || result.StructuredContent["tr"] != "target-next" {
		t.Fatalf("text replace = %+v, %v", result, err)
	}
	request := application.applyRequest
	if request.ExpectedDocumentRevision != "current" || request.ExpectedTargetRevision == nil || *request.ExpectedTargetRevision != target {
		t.Fatalf("apply must carry the revisions it read: %+v", request)
	}
}

func TestAddingABlockNeedsNoRevisionsAndStillCarriesTheCurrentOnes(t *testing.T) {
	for _, tool := range []struct{ name, arguments string }{
		{ToolParagraphCreate, `"text":"Hello"`},
		{ToolHeadingCreate, `"text":"Hello","level":2`},
		{ToolTableCreate, `"rows":[["a"]]`},
	} {
		t.Run(tool.name, func(t *testing.T) {
			application := &recordingAIDocumentApplication{
				openResult:  core.OpenMetadata{DocumentRevision: "current"},
				applyResult: core.ApplyResult{DocumentRevision: "next", Changed: true, Changes: []core.Change{{Kind: core.OperationInsertBlock, AffectedHandles: []string{"block:new"}}}},
				readResult:  core.Projection{DocumentRevision: "current"},
			}
			result, err := mustAIDocumentTools(t, application).CallTool(t.Context(), mcpserver.Principal{}, tool.name, toolArguments(t,
				`{"document_type":"post","document_id":"`+syncTestDocument+`","locale":"ko",`+tool.arguments+`}`))
			if err != nil || result.IsError {
				t.Fatalf("%s = %+v, %v", tool.name, result, err)
			}
			if application.applyRequest.ExpectedDocumentRevision != "current" {
				t.Fatalf("apply revision = %q", application.applyRequest.ExpectedDocumentRevision)
			}
		})
	}
}

func TestReplacingABlockStillRequiresARevision(t *testing.T) {
	application := &recordingAIDocumentApplication{openResult: core.OpenMetadata{DocumentRevision: "current"}}
	result, err := mustAIDocumentTools(t, application).CallTool(t.Context(), mcpserver.Principal{}, ToolParagraphUpdate, toolArguments(t,
		`{"document_type":"post","document_id":"`+syncTestDocument+`","locale":"ko","block_id":"a","text":"Hello"}`))
	if err == nil && !result.IsError && application.applyCalls != 0 {
		t.Fatalf("an overwrite with no revision was applied: %+v", result)
	}
}

func TestRejectedBatchDoesNotEchoTheOperations(t *testing.T) {
	encoded, err := encodeValidation(core.ValidationResult{
		Normalized: []core.Operation{core.DeleteBlockOperation("a")},
		Issues:     []core.OperationIssue{{Operation: 0, Code: core.IssueInvalidOperation, Message: "no"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"o"`) {
		t.Fatalf("rejection echoed the batch: %s", encoded)
	}
}

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
