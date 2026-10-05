package mcp

import (
	"reflect"
	"testing"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
)

func TestPageMetadataLayoutBuildsNativeTypedObject(t *testing.T) {
	application := &recordingAIDocumentApplication{applyResult: core.ApplyResult{DocumentRevision: "next"}}
	input := toolArguments(t, `{"document_type":"page","document_id":"44444444-4444-4444-8444-444444444444","locale":"ko","expected_document_revision":"before","title":"Title","document_layout":{"content_height":"DOCUMENT_CONTENT_HEIGHT_VIEWPORT","page_chrome":"DOCUMENT_REGION_PLACEMENT_FLOW","footer":"DOCUMENT_REGION_PLACEMENT_PINNED"}}`)
	_, err := mustAIDocumentTools(t, application).CallTool(t.Context(), mcpserver.Principal{}, ToolMetadataUpdate, input)
	if err != nil {
		t.Fatal(err)
	}
	want := []core.Operation{
		core.SetFieldOperation("document", "title", core.Text("Title")),
		core.SetFieldOperation("document", "documentLayout", core.Object(
			core.ObjectValue("contentHeight", core.Text("DOCUMENT_CONTENT_HEIGHT_VIEWPORT")),
			core.ObjectValue("pageChrome", core.Text("DOCUMENT_REGION_PLACEMENT_FLOW")),
			core.ObjectValue("footer", core.Text("DOCUMENT_REGION_PLACEMENT_PINNED")),
		)),
	}
	if !reflect.DeepEqual(want, application.applyRequest.Operations) {
		t.Fatalf("layout operations: %+v", application.applyRequest.Operations)
	}
	for _, invalid := range []string{
		`{"content_height":"viewport","page_chrome":"DOCUMENT_REGION_PLACEMENT_FLOW","footer":"DOCUMENT_REGION_PLACEMENT_PINNED"}`,
		`{"content_height":"DOCUMENT_CONTENT_HEIGHT_VIEWPORT","page_chrome":"DOCUMENT_REGION_PLACEMENT_FLOW"}`,
		`null`,
	} {
		input["document_layout"] = []byte(invalid)
		application.applyCalls = 0
		_, err = mustAIDocumentTools(t, application).CallTool(t.Context(), mcpserver.Principal{}, ToolMetadataUpdate, input)
		if err == nil || application.applyCalls != 0 {
			t.Fatalf("invalid layout applied: %s, err=%v", invalid, err)
		}
	}
}

func TestReleaseMetadataTitleUsesLocalizedDocumentHandle(t *testing.T) {
	application := &recordingAIDocumentApplication{applyResult: core.ApplyResult{DocumentRevision: "next"}}
	input := toolArguments(t, `{"document_type":"release","document_id":"44444444-4444-4444-8444-444444444444","locale":"ja","expected_document_revision":"source","expected_target_revision":"target","title":"Localized title"}`)
	_, err := mustAIDocumentTools(t, application).CallTool(t.Context(), mcpserver.Principal{}, ToolMetadataUpdate, input)
	if err != nil {
		t.Fatal(err)
	}
	request := application.applyRequest
	if request.Profile != core.DomainRelease || request.Locale != "ja" || request.ExpectedTargetRevision == nil || *request.ExpectedTargetRevision != "target" || !reflect.DeepEqual(request.Operations, []core.Operation{core.SetFieldOperation("document", "title", core.Text("Localized title"))}) {
		t.Fatalf("release title request: %+v", request)
	}
	input["summary"] = []byte(`"unsupported"`)
	application.applyCalls = 0
	_, err = mustAIDocumentTools(t, application).CallTool(t.Context(), mcpserver.Principal{}, ToolMetadataUpdate, input)
	if err == nil || application.applyCalls != 0 {
		t.Fatalf("unsupported release summary applied: %v", err)
	}
}
