package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	core "github.com/echovisionlab/geul-api/internal/aidocument"
)

func TestDocumentReadInvalidConditionsThroughHTTPAreToolErrors(t *testing.T) {
	for _, tc := range []struct {
		name, selectors, mode, message string
		loads                          int
	}{
		{"missing fields", "", "fields", "field read requires field selectors only", 0},
		{"empty fields", `,"fields":[]`, "fields", "field read requires field selectors only", 0},
		{"missing blocks", "", "blocks", "block read requires block selectors only", 0},
		{"empty blocks", `,"block_ids":[]`, "blocks", "block read requires block selectors only", 0},
		{"outline selectors", `,"block_ids":["private-block"]`, "outline", "outline read does not accept block or field selectors", 0},
		{"mixed selectors", `,"block_ids":["private-block"],"fields":[{"block_id":"private-block","field":"title"}]`, "fields", "field read requires field selectors only", 0},
		{"unknown block", `,"block_ids":["missing-block"]`, "blocks", "one or more selected blocks do not exist", 1},
		{"unknown field block", `,"fields":[{"block_id":"missing-block","field":"title"}]`, "fields", `selected block "missing-block" does not exist`, 1},
		{"unknown field", `,"fields":[{"block_id":"private-block","field":"missing-field"}]`, "fields", `selected field "missing-field" is not defined`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			port := &documentCatalogPort{document: documentCatalogFixture()}
			service, err := core.NewService(port)
			if err != nil {
				t.Fatal(err)
			}
			tools, err := NewAIDocumentTools(service)
			if err != nil {
				t.Fatal(err)
			}
			body := callDocumentReadHTTP(t, tools, `"mode":"`+tc.mode+`"`+tc.selectors)
			if body["error"] != nil {
				t.Fatalf("input error became JSON-RPC error: %#v", body)
			}
			result, ok := body["result"].(map[string]any)
			if !ok || result["isError"] != true {
				t.Fatalf("expected actionable tool error: %#v", body)
			}
			text := result["content"].([]any)[0].(map[string]any)["text"].(string)
			if !strings.Contains(text, tc.message) || port.loads != tc.loads {
				t.Fatalf("text=%q loads=%d, want %q/%d", text, port.loads, tc.message, tc.loads)
			}
		})
	}
}

func TestDocumentReadHTTPPreservesPermissionAndInternalFailures(t *testing.T) {
	for _, tc := range []struct {
		name            string
		loadErr         error
		invalidDocument bool
		internal        bool
	}{
		{name: "permission denied", loadErr: connect.NewError(connect.CodePermissionDenied, errors.New("permission denied"))},
		{name: "not found", loadErr: connect.NewError(connect.CodeNotFound, errors.New("document not found"))},
		{name: "internal dependency", loadErr: errors.New("private dependency credential"), internal: true},
		{name: "invalid loaded document", invalidDocument: true, internal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			port := &documentCatalogPort{document: documentCatalogFixture(), loadErr: tc.loadErr}
			if tc.invalidDocument {
				port.document.Catalog.Fingerprint = ""
			}
			service, err := core.NewService(port)
			if err != nil {
				t.Fatal(err)
			}
			tools, err := NewAIDocumentTools(service)
			if err != nil {
				t.Fatal(err)
			}
			body := callDocumentReadHTTP(t, tools, `"mode":"outline"`)
			if tc.internal {
				rpc, ok := body["error"].(map[string]any)
				if !ok || rpc["code"] != float64(-32603) || rpc["message"] != "Internal error" {
					t.Fatalf("internal failure was misclassified/leaked: %#v", body)
				}
			} else if body["error"] != nil || body["result"].(map[string]any)["isError"] != true {
				t.Fatalf("expected domain failure became internal: %#v", body)
			}
		})
	}
}

func TestDocumentReadMapsWrappedInputError(t *testing.T) {
	application := &recordingAIDocumentApplication{readError: fmt.Errorf("private context: %w", &core.InputError{Message: "field read requires field selectors only"})}
	body := callDocumentReadHTTP(t, mustAIDocumentTools(t, application).AIDocumentTools, `"mode":"fields"`)
	wire, _ := json.Marshal(body)
	if body["error"] != nil || !strings.Contains(string(wire), "field read requires") || strings.Contains(string(wire), "private context") {
		t.Fatalf("wrapped input error=%s", wire)
	}
}

func callDocumentReadHTTP(t *testing.T, tools *AIDocumentTools, selectors string) map[string]any {
	t.Helper()
	config := validHTTPConfig(nil)
	config.Registry = tools
	config.Dispatcher = tools
	response := httptest.NewRecorder()
	newHTTPTestHandler(t, config).ServeHTTP(response, mcpHTTPRequest(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"document_read","arguments":{"document_type":"page","document_id":"`+syncTestDocument+`","locale":"ko",`+selectors+`}}}`))
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 {
		t.Fatalf("unexpected HTTP status %d: %s", response.Code, response.Body.String())
	}
	return body
}
