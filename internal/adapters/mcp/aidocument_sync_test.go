package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect"
	core "github.com/echovisionlab/geul-api/internal/aidocument"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
)

const syncTestDocument = "44444444-4444-4444-8444-444444444444"

// Check the observable contract, including the text representation used by clients
// that do not consume structuredContent.
func assertDocumentSync(t *testing.T, result mcpserver.ToolResult, reason, document string, target *core.Revision, handles []string, discard bool, arguments map[string]any) {
	t.Helper()
	if result.IsError || len(result.Content) != 1 {
		t.Fatalf("sync result = %+v", result)
	}
	var text map[string]any
	if err := json.Unmarshal([]byte(result.Content[0]["text"].(string)), &text); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(text, result.StructuredContent) {
		t.Fatalf("text and structured content differ: %+v", result)
	}
	var targetValue any
	if target != nil {
		targetValue = string(*target)
	}
	handleValues := make([]any, len(handles))
	for i, h := range handles {
		handleValues[i] = h
	}
	want := map[string]any{
		"status": "sync_required", "reason": reason, "applied": false,
		"current_document_revision": document, "current_target_revision": targetValue,
		"affected_handles": handleValues, "discard_previous_pages": discard,
		"read": map[string]any{"tool": ToolDocumentRead, "arguments": arguments},
	}
	for key, value := range want {
		if !reflect.DeepEqual(text[key], value) {
			t.Errorf("sync %s = %#v, want %#v", key, text[key], value)
		}
	}
	if instruction, ok := text["instructions"].(string); !ok || strings.TrimSpace(instruction) == "" {
		t.Error("sync instructions must be nonempty")
	}
	if len(text) != len(want)+1 {
		t.Errorf("unexpected sync fields: %#v", text)
	}
}

func TestDocumentReadSyncPreservesSelectorsAndDropsOnlyContinuation(t *testing.T) {
	target := core.Revision("target-current")
	for _, wrapped := range []bool{false, true} {
		t.Run(fmt.Sprint(wrapped), func(t *testing.T) {
			var cursorErr error = &core.CursorError{Code: "read_revision_conflict", CurrentDocumentRevision: "document-current", CurrentTargetRevision: &target}
			if wrapped {
				cursorErr = fmt.Errorf("read failed: %w", cursorErr)
			}
			application := &recordingAIDocumentApplication{readError: cursorErr}
			arguments := toolArguments(t, `{"p":"post","d":"`+syncTestDocument+`","l":"en","m":"fields","b":["paragraph-a"],"f":[["paragraph-a","content"]],"n":256,"c":"expired"}`)
			result, err := mustAIDocumentTools(t, application).CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentRead, arguments)
			if err != nil {
				t.Fatal(err)
			}
			recipe := map[string]any{}
			inputJSON, _ := json.Marshal(arguments)
			json.Unmarshal(inputJSON, &recipe)
			delete(recipe, "c")
			assertDocumentSync(t, result, "read_continuation_invalid", "document-current", &target, nil, true, recipe)
			if application.readCalls != 1 || application.readRequest.Cursor != "expired" {
				t.Fatalf("read retried or cursor replaced: %+v", application)
			}
		})
	}
}

func syncMutationArguments(t *testing.T) mcpserver.ToolArguments {
	t.Helper()
	encoded, err := core.EncodeApplyRequest(core.ApplyRequest{Protocol: core.ProtocolVersion, Profile: core.DomainPost, Document: syncTestDocument, Locale: "en", ExpectedDocumentRevision: "document-stale", ExpectedTargetRevision: revisionPointer("target-stale"), Operations: []core.Operation{core.SetFieldOperation("paragraph-a", "content", core.Text("replacement"))}})
	if err != nil {
		t.Fatal(err)
	}
	return toolArguments(t, string(encoded))
}
func revisionPointer(value core.Revision) *core.Revision { return &value }

func TestDocumentMutationSyncCoversConflictCarriers(t *testing.T) {
	for _, tool := range []string{ToolDocumentApply, ToolDocumentValidate} {
		for _, code := range []core.ConflictCode{core.ConflictDocumentRevision, core.ConflictTargetRevision} {
			for _, carrier := range []string{"typed", "wrapped", "validation-error", "returned-validation"} {
				if tool == ToolDocumentApply && carrier == "returned-validation" {
					continue
				}
				t.Run(tool+"/"+string(code)+"/"+carrier, func(t *testing.T) {
					target := core.Revision("target-current")
					conflict := core.Conflict{Code: code, CurrentDocumentRevision: "document-current", CurrentTargetRevision: &target, AffectedHandles: []string{"field:paragraph-a/content"}}
					var rejection error = &core.ConflictError{Conflict: conflict}
					if carrier == "wrapped" {
						rejection = fmt.Errorf("application: %w", rejection)
					}
					if carrier == "validation-error" {
						rejection = fmt.Errorf("validation: %w", &core.ValidationError{Result: core.ValidationResult{Conflict: &conflict}})
					}
					application := &recordingAIDocumentApplication{}
					if tool == ToolDocumentApply {
						application.applyError = rejection
					} else if carrier == "returned-validation" {
						application.validateResult.Conflict = &conflict
					} else {
						application.validateError = rejection
					}
					result, err := mustAIDocumentTools(t, application).CallTool(t.Context(), mcpserver.Principal{}, tool, syncMutationArguments(t))
					if err != nil {
						t.Fatal(err)
					}
					reason := "document_revision_changed"
					if code == core.ConflictTargetRevision {
						reason = "target_revision_changed"
					}
					assertDocumentSync(t, result, reason, "document-current", &target, conflict.AffectedHandles, false, map[string]any{"p": "post", "d": syncTestDocument, "l": "en", "m": "outline"})
					request := application.applyRequest
					if tool == ToolDocumentValidate {
						request = application.validateRequest
					}
					if request.ExpectedDocumentRevision != "document-stale" || *request.ExpectedTargetRevision != "target-stale" {
						t.Fatalf("CAS changed: %+v", request)
					}
					if application.applyCalls > 1 || application.readCalls != 0 {
						t.Fatalf("automatic retry/read: %+v", application)
					}
				})
			}
		}
	}
}

func TestFocusedDocumentMutationUsesSyncContract(t *testing.T) {
	conflict := core.Conflict{Code: core.ConflictDocumentRevision, CurrentDocumentRevision: "current", AffectedHandles: []string{"field:paragraph-a/content"}}
	application := &recordingAIDocumentApplication{applyError: &core.ConflictError{Conflict: conflict}}
	result, err := mustAIDocumentTools(t, application).CallTool(t.Context(), mcpserver.Principal{}, ToolParagraphUpdate, toolArguments(t, `{"document_type":"post","document_id":"`+syncTestDocument+`","locale":"ko","expected_document_revision":"stale","block_id":"paragraph-a","text":"replacement"}`))
	if err != nil {
		t.Fatal(err)
	}
	assertDocumentSync(t, result, "document_revision_changed", "current", nil, conflict.AffectedHandles, false, map[string]any{"p": "post", "d": syncTestDocument, "l": "ko", "m": "outline"})
	if application.applyCalls != 1 || application.applyRequest.ExpectedDocumentRevision != "stale" {
		t.Fatal("focused mutation retried or replaced CAS")
	}
}

func TestDocumentReadStaleCursorThroughHTTPReturnsNormalResult(t *testing.T) {
	tools := mustAIDocumentTools(t, &recordingAIDocumentApplication{readError: &core.CursorError{Code: "invalid_cursor", CurrentDocumentRevision: "current"}})
	config := validHTTPConfig(nil)
	config.Registry = tools
	config.Dispatcher = tools
	response := httptest.NewRecorder()
	newHTTPTestHandler(t, config).ServeHTTP(response, mcpHTTPRequest(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"document_read","arguments":{"p":"post","d":"`+syncTestDocument+`","l":"ko","m":"outline","c":"expired"}}}`))
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || body["error"] != nil {
		t.Fatalf("HTTP sync = %d %s", response.Code, response.Body.String())
	}
	result := body["result"].(map[string]any)
	if result["isError"] == true || result["structuredContent"].(map[string]any)["status"] != "sync_required" {
		t.Fatalf("HTTP result = %#v", result)
	}
}

func TestDocumentMutationRequiresExplicitFreshRequestAfterSync(t *testing.T) {
	application := &recordingAIDocumentApplication{applyError: &core.ConflictError{Conflict: core.Conflict{Code: core.ConflictTargetRevision, CurrentDocumentRevision: "document-stale", CurrentTargetRevision: revisionPointer("target-current")}}}
	tools := mustAIDocumentTools(t, application)
	args := syncMutationArguments(t)
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentApply, args)
	if err != nil || result.IsError || application.applyCalls != 1 || *application.applyRequest.ExpectedTargetRevision != "target-stale" {
		t.Fatalf("stale apply = %+v, %v", result, err)
	}
	// Only the client's explicit read and revised write can recover the conflict.
	application.readResult = core.Projection{Protocol: core.ProtocolVersion, Profile: core.DomainPost, Catalog: "catalog", Document: syncTestDocument, DocumentRevision: "document-stale", TargetRevision: revisionPointer("target-current"), SourceLocale: "ko", Locale: "en", LocaleRole: core.LocaleRoleNonSource, LocaleExists: true, Mode: core.ReadOutline}
	recipe := result.StructuredContent["read"].(map[string]any)["arguments"].(map[string]any)
	encoded, _ := json.Marshal(recipe)
	read, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentRead, toolArguments(t, string(encoded)))
	if err != nil || read.StructuredContent["tr"] != "target-current" {
		t.Fatalf("reread = %+v, %v", read, err)
	}
	application.readResult.Mode = core.ReadFields
	application.readResult.Nodes = []core.Node{{ID: "paragraph-a", Kind: "paragraph", Localized: []core.FieldValue{{ID: "content", Value: core.Text("Concurrent value")}}}}
	latest, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentRead, toolArguments(t, `{"p":"post","d":"`+syncTestDocument+`","l":"en","m":"fields","f":[["paragraph-a","content"]]}`))
	if err != nil || !strings.Contains(latest.Content[0]["text"].(string), "Concurrent value") {
		t.Fatalf("latest targets = %+v, %v", latest, err)
	}
	// The client reassesses the edit and preserves the concurrent value rather
	// than resubmitting the rejected replacement with an updated CAS alone.
	reassessed := core.SetFieldOperation("paragraph-a", "content", core.Text("Concurrent value; replacement"))
	fresh, err := core.EncodeApplyRequest(core.ApplyRequest{Protocol: core.ProtocolVersion, Profile: core.DomainPost, Document: syncTestDocument, Locale: "en", ExpectedDocumentRevision: "document-stale", ExpectedTargetRevision: revisionPointer("target-current"), Operations: []core.Operation{reassessed}})
	if err != nil {
		t.Fatal(err)
	}
	args = toolArguments(t, string(fresh))
	application.applyError = nil
	application.applyResult = core.ApplyResult{DocumentRevision: "document-stale", TargetRevision: revisionPointer("target-next"), Changed: true}
	applied, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentApply, args)
	if err != nil || applied.IsError || applied.StructuredContent["tr"] != "target-next" || application.applyCalls != 2 || application.readCalls != 2 || *application.applyRequest.ExpectedTargetRevision != "target-current" || !reflect.DeepEqual(application.applyRequest.Operations, []core.Operation{reassessed}) {
		t.Fatalf("explicit recovery = %+v, %v; application=%+v", applied, err, application)
	}
}

func TestDocumentSyncDoesNotSwallowRealErrors(t *testing.T) {
	for _, tool := range []string{ToolDocumentValidate, ToolDocumentApply} {
		for _, code := range []connect.Code{connect.CodeInvalidArgument, connect.CodePermissionDenied, connect.CodeUnauthenticated, connect.CodeInternal} {
			t.Run(tool+"/"+code.String(), func(t *testing.T) {
				rejection := connect.NewError(code, errors.New("application rejected request"))
				application := &recordingAIDocumentApplication{applyError: rejection, validateError: rejection}
				result, err := mustAIDocumentTools(t, application).CallTool(t.Context(), mcpserver.Principal{}, tool, syncMutationArguments(t))
				if err == nil || result.Content != nil {
					t.Fatalf("real error became sync: %+v, %v", result, err)
				}
				var execution *mcpserver.ToolExecutionError
				if errors.As(err, &execution) != (code != connect.CodeInternal) {
					t.Fatalf("error classification changed: %v", err)
				}
			})
		}
	}
}

func TestDocumentSyncSchemasPreserveSuccessAndRootReferences(t *testing.T) {
	tools := mustAIDocumentTools(t, &recordingAIDocumentApplication{})
	listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	if err != nil {
		t.Fatal(err)
	}
	fileTools := mustFileBlockTools(t, &recordingAIDocumentApplication{}, &recordingFileBlockManagement{})
	files, err := fileTools.ListTools(t.Context(), mcpserver.Principal{})
	if err != nil {
		t.Fatal(err)
	}
	listed = append(listed, files...)
	originals := map[string]string{ToolDocumentRead: projectionOutputJSONSchema, ToolParagraphCreate: focusedMutationOutputJSONSchema, ToolParagraphUpdate: focusedMutationOutputJSONSchema, ToolBlockDelete: focusedMutationOutputJSONSchema, ToolMetadataUpdate: focusedMutationOutputJSONSchema, ToolDocumentApply: acceptedOutputJSONSchema, ToolDocumentValidate: validationOutputJSONSchema, ToolDocumentFileAdd: documentFileMutationOutputJSONSchema, ToolDocumentFileReplace: documentFileMutationOutputJSONSchema, ToolDocumentFileRemove: documentFileMutationOutputJSONSchema}
	for _, tool := range listed {
		original, ok := originals[tool.Name]
		if !ok {
			continue
		}
		t.Run(tool.Name, func(t *testing.T) {
			var root map[string]any
			if err := json.Unmarshal(tool.OutputSchema, &root); err != nil {
				t.Fatal(err)
			}
			variants, ok := root["oneOf"].([]any)
			if !ok || len(variants) != 2 {
				t.Fatalf("output must admit success and sync: %s", tool.OutputSchema)
			}
			if original != "" {
				var expected map[string]any
				json.Unmarshal([]byte(original), &expected)
				defs := expected["$defs"]
				delete(expected, "$defs")
				if !reflect.DeepEqual(variants[0], expected) || !reflect.DeepEqual(root["$defs"], defs) {
					t.Error("normal success schema or root definitions changed")
				}
			}
			sync := variants[1].(map[string]any)
			properties := sync["properties"].(map[string]any)
			if sync["additionalProperties"] != false || len(sync["required"].([]any)) != 9 || len(properties) != 9 || properties["status"].(map[string]any)["const"] != "sync_required" || properties["applied"].(map[string]any)["const"] != false {
				t.Fatalf("sync schema contract = %#v", sync)
			}
			recipe := properties["read"].(map[string]any)["properties"].(map[string]any)["arguments"].(map[string]any)["properties"].(map[string]any)
			if recipe["c"] != nil || recipe["n"].(map[string]any)["maximum"] != float64(256) {
				t.Fatalf("read recipe must restart and admit valid page limit: %#v", recipe)
			}
			assertSchemaRootRefsResolve(t, root, root)
		})
	}
}

// No schema validator dependency is added. Check every actual local reference
// against the advertised root; recursive projection references are particularly
// vulnerable when a success schema is moved into a union alternative.
func assertSchemaRootRefsResolve(t *testing.T, root map[string]any, value any) {
	t.Helper()
	switch value := value.(type) {
	case map[string]any:
		if ref, ok := value["$ref"].(string); ok {
			if !strings.HasPrefix(ref, "#/") {
				t.Fatalf("unsupported schema reference %q", ref)
			}
			var target any = root
			for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
				object, ok := target.(map[string]any)
				if !ok {
					t.Fatalf("reference %q traverses nonobject", ref)
				}
				target, ok = object[strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")]
				if !ok {
					t.Fatalf("reference %q does not resolve at schema root", ref)
				}
			}
		}
		for _, nested := range value {
			assertSchemaRootRefsResolve(t, root, nested)
		}
	case []any:
		for _, nested := range value {
			assertSchemaRootRefsResolve(t, root, nested)
		}
	}
}

func TestDocumentValidateRealValidationIssueDoesNotBecomeSync(t *testing.T) {
	issue := core.OperationIssue{Operation: 0, Code: core.IssueTargetFieldForbidden, Handle: "field:paragraph-a/content", Message: "target locale cannot mutate shared field"}
	application := &recordingAIDocumentApplication{validateError: &core.ValidationError{Result: core.ValidationResult{Issues: []core.OperationIssue{issue}}}}
	result, err := mustAIDocumentTools(t, application).CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentValidate, syncMutationArguments(t))
	if err == nil || result.Content != nil {
		t.Fatalf("validation error became sync: %+v, %v", result, err)
	}
	var validation *core.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("validation error classification changed: %v", err)
	}
}
