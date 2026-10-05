package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect"
	core "github.com/echovisionlab/geul-api/internal/aidocument"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/google/uuid"
)

func TestAIDocumentToolsListCompactTypedSurface(t *testing.T) {
	application := &recordingAIDocumentApplication{}
	tools, err := NewAIDocumentTools(application)
	if err != nil {
		t.Fatalf("NewAIDocumentTools() error = %v", err)
	}

	listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	wantNames := []string{
		ToolDocumentOpen, ToolDocumentRead,
		ToolParagraphCreate, ToolParagraphUpdate, ToolBlockDelete,
		ToolMetadataUpdate,
		ToolDocumentValidate, ToolDocumentApply,
	}
	wantAnnotations := map[string]map[string]any{
		ToolDocumentOpen:     toolAnnotations(true, false, false),
		ToolDocumentRead:     toolAnnotations(true, false, false),
		ToolParagraphCreate:  toolAnnotations(false, false, false),
		ToolParagraphUpdate:  toolAnnotations(false, true, false),
		ToolBlockDelete:      toolAnnotations(false, true, false),
		ToolMetadataUpdate:   toolAnnotations(false, true, false),
		ToolDocumentValidate: toolAnnotations(true, false, false),
		ToolDocumentApply:    toolAnnotations(false, true, false),
	}
	if len(listed) != len(wantNames) {
		t.Fatalf("ListTools() returned %d tools, want %d", len(listed), len(wantNames))
	}
	for index, tool := range listed {
		if tool.Name != wantNames[index] {
			t.Fatalf("tool %d name = %q, want %q", index, tool.Name, wantNames[index])
		}
		assertMCPToolOAuthSecurity(t, tool)
		assertMCPToolAnnotations(t, tool, wantAnnotations[tool.Name])
		for schemaName, schema := range map[string]json.RawMessage{
			"input": tool.InputSchema, "output": tool.OutputSchema,
		} {
			var object map[string]any
			if err := json.Unmarshal(schema, &object); err != nil || object["type"] != "object" {
				t.Fatalf("%s %s schema is not an object schema: %s (%v)", tool.Name, schemaName, schema, err)
			}
			lower := strings.ToLower(string(schema))
			for _, forbidden := range []string{"html", "tiptap", "prosemirror", "yjs", "xliff", "base64"} {
				if strings.Contains(lower, forbidden) {
					t.Fatalf("%s %s schema exposed forbidden representation %q", tool.Name, schemaName, forbidden)
				}
			}
		}
	}

	listed[0].InputSchema[0] = '['
	listed[0].SecuritySchemes[0].Scopes[0] = "other"
	listed[0].Meta["securitySchemes"].([]mcpserver.ToolSecurityScheme)[0].Scopes[0] = "other"
	listed[0].Annotations["readOnlyHint"] = false
	again, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	if err != nil {
		t.Fatalf("second ListTools() error = %v", err)
	}
	if again[0].InputSchema[0] != '{' || again[0].SecuritySchemes[0].Scopes[0] != "mcp" ||
		again[0].Meta["securitySchemes"].([]mcpserver.ToolSecurityScheme)[0].Scopes[0] != "mcp" ||
		again[0].Annotations["readOnlyHint"] != true {
		t.Fatal("ListTools() returned mutable shared tool definitions")
	}
}

func TestDocumentMetadataUpdateBuildsFocusedExactOperations(t *testing.T) {
	application := &recordingAIDocumentApplication{applyResult: core.ApplyResult{
		DocumentRevision: "revision-b", Changed: true,
		Changes: []core.Change{{Operation: 1, Kind: core.OperationUnsetField, AffectedHandles: []string{"field:document/summary"}}},
	}}
	tools := mustAIDocumentTools(t, application)
	categoryID := "11111111-1111-4111-8111-111111111111"
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMetadataUpdate, toolArguments(t, `{
		"document_type":"post","document_id":"44444444-4444-4444-8444-444444444444",
		"locale":"ko","expected_document_revision":"revision-a",
		"title":"새 제목","clear_summary":true,"category_ids":["`+categoryID+`"],"tag_ids":[]
	}`))
	if err != nil {
		t.Fatalf("document_metadata_update error = %v", err)
	}
	if result.StructuredContent["dr"] != "revision-b" {
		t.Fatalf("document_metadata_update result = %#v", result.StructuredContent)
	}
	var outputSchema struct {
		Properties struct {
			Changes struct {
				Items struct {
					PrefixItems []struct {
						Enum []string `json:"enum"`
					} `json:"prefixItems"`
				} `json:"items"`
			} `json:"c"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(focusedMutationOutputJSONSchema), &outputSchema); err != nil {
		t.Fatal(err)
	}
	change := result.StructuredContent["c"].([]any)[0].([]any)
	if change[1] != string(core.OperationUnsetField) {
		t.Fatalf("clear summary change=%v", change)
	}
	allowed := false
	for _, kind := range outputSchema.Properties.Changes.Items.PrefixItems[1].Enum {
		if kind == change[1] {
			allowed = true
		}
	}
	if !allowed {
		t.Fatalf("accepted clear summary kind %q excluded by output schema", change[1])
	}
	request := application.applyRequest
	if request.Profile != core.DomainPost || request.Document != "44444444-4444-4444-8444-444444444444" || request.ExpectedDocumentRevision != "revision-a" {
		t.Fatalf("metadata request identity = %#v", request)
	}
	if len(request.Operations) != 4 {
		t.Fatalf("metadata operations = %#v", request.Operations)
	}
	if request.Operations[0].SetField.Target.Block != "document" || request.Operations[0].SetField.Target.Field != "title" || request.Operations[0].SetField.Value.Text != "새 제목" {
		t.Fatalf("title operation = %#v", request.Operations[0])
	}
	if request.Operations[1].Kind != core.OperationUnsetField || request.Operations[1].UnsetField.Target.Field != "summary" {
		t.Fatalf("summary operation = %#v", request.Operations[1])
	}
	if got := request.Operations[2].SetField.Value.List; len(got) != 1 || got[0].ID != core.RelationItemID(categoryID) || got[0].Value.Text != categoryID {
		t.Fatalf("category operation = %#v", request.Operations[2])
	}
	if got := request.Operations[3].SetField.Value.List; len(got) != 0 {
		t.Fatalf("tag operation = %#v", request.Operations[3])
	}
}

func TestDocumentMetadataUpdateRejectsNullInsteadOfApplyingOtherFields(t *testing.T) {
	for _, field := range []string{"title", "summary", "clear_summary", "category_ids", "tag_ids"} {
		t.Run(field, func(t *testing.T) {
			application := &recordingAIDocumentApplication{applyResult: core.ApplyResult{DocumentRevision: "revision-b"}}
			arguments := toolArguments(t, `{
				"document_type":"post","document_id":"44444444-4444-4444-8444-444444444444",
				"locale":"ko","expected_document_revision":"revision-a","title":"Changed","tag_ids":[]
			}`)
			arguments[field] = json.RawMessage("null")
			result, err := mustAIDocumentTools(t, application).CallTool(t.Context(), mcpserver.Principal{}, ToolMetadataUpdate, arguments)
			var execution *mcpserver.ToolExecutionError
			if !errors.As(err, &execution) || application.applyCalls != 0 || result.Content != nil {
				t.Fatalf("null %s partially applied metadata: calls=%d result=%+v error=%v", field, application.applyCalls, result, err)
			}
		})
	}
}

func TestParagraphCreateRejectsNullPlacementHandles(t *testing.T) {
	for _, field := range []string{"parent_block_id", "after_block_id"} {
		t.Run(field, func(t *testing.T) {
			application := &recordingAIDocumentApplication{applyResult: core.ApplyResult{DocumentRevision: "revision-b"}}
			arguments := toolArguments(t, `{
				"document_type":"post","document_id":"44444444-4444-4444-8444-444444444444",
				"locale":"ko","expected_document_revision":"revision-a","text":"New paragraph"
			}`)
			arguments[field] = json.RawMessage("null")
			_, err := mustAIDocumentTools(t, application).CallTool(t.Context(), mcpserver.Principal{}, ToolParagraphCreate, arguments)
			var execution *mcpserver.ToolExecutionError
			if !errors.As(err, &execution) || application.applyCalls != 0 {
				t.Fatalf("null %s became root/first placement: calls=%d error=%v", field, application.applyCalls, err)
			}
		})
	}
}

func TestMutationToolsDeclareStructuredValidationRejections(t *testing.T) {
	operation := core.SetFieldOperation("paragraph-a", "content", core.Text("wrong kind"))
	for _, name := range []string{ToolParagraphCreate, ToolParagraphUpdate, ToolBlockDelete, ToolMetadataUpdate, ToolDocumentApply} {
		t.Run(name, func(t *testing.T) {
			application := &recordingAIDocumentApplication{applyError: &core.ValidationError{Result: core.ValidationResult{
				Normalized: []core.Operation{operation},
				Issues:     []core.OperationIssue{{Operation: 0, Code: core.IssueValueKindMismatch, Handle: "field:paragraph-a/content", Message: "wrong value kind"}},
			}}}
			arguments := toolArguments(t, `{"document_type":"post","document_id":"44444444-4444-4444-8444-444444444444","locale":"ko","expected_document_revision":"revision-a"}`)
			switch name {
			case ToolParagraphCreate:
				arguments["text"] = json.RawMessage(`"paragraph"`)
			case ToolParagraphUpdate:
				arguments["text"] = json.RawMessage(`"paragraph"`)
				arguments["block_id"] = json.RawMessage(`"paragraph-a"`)
			case ToolBlockDelete:
				arguments["block_id"] = json.RawMessage(`"paragraph-a"`)
			case ToolMetadataUpdate:
				arguments["title"] = json.RawMessage(`"Changed"`)
			case ToolDocumentApply:
				arguments = toolArguments(t, `{"v":"dcdp/1","p":"post","d":"44444444-4444-4444-8444-444444444444","l":"ko","edr":"revision-a","o":[["fs",["paragraph-a","","","content"],["t","wrong kind"]]]}`)
			}
			tools := mustAIDocumentTools(t, application)
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, name, arguments)
			if err != nil || !result.IsError || result.StructuredContent["i"] == nil || result.StructuredContent["o"] == nil {
				t.Fatalf("missing structured rejection: %+v %v", result, err)
			}
			listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
			if err != nil {
				t.Fatal(err)
			}
			for _, tool := range listed {
				if tool.Name != name {
					continue
				}
				var schema struct {
					OneOf []struct {
						OneOf []struct {
							Properties map[string]json.RawMessage `json:"properties"`
						} `json:"oneOf"`
					} `json:"oneOf"`
				}
				if err := json.Unmarshal(tool.OutputSchema, &schema); err != nil {
					t.Fatal(err)
				}
				declared := false
				for _, branch := range schema.OneOf {
					for _, resultBranch := range branch.OneOf {
						if resultBranch.Properties["i"] != nil && resultBranch.Properties["o"] != nil {
							declared = true
						}
					}
				}
				if !declared {
					t.Fatalf("actual i/o validation rejection excluded by %s output schema: %s", name, tool.OutputSchema)
				}
			}
		})
	}
}

func assertMCPToolOAuthSecurity(t *testing.T, tool mcpserver.Tool) {
	t.Helper()
	if len(tool.SecuritySchemes) != 1 || tool.SecuritySchemes[0].Type != "oauth2" ||
		!reflect.DeepEqual(tool.SecuritySchemes[0].Scopes, []string{"mcp", "offline_access"}) {
		t.Fatalf("%s securitySchemes = %#v, want oauth2 mcp offline_access", tool.Name, tool.SecuritySchemes)
	}
	mirrored, ok := tool.Meta["securitySchemes"].([]mcpserver.ToolSecurityScheme)
	if !ok || !reflect.DeepEqual(mirrored, tool.SecuritySchemes) {
		t.Fatalf("%s _meta.securitySchemes = %#v, want exact top-level mirror", tool.Name, tool.Meta["securitySchemes"])
	}
}

func assertMCPToolAnnotations(t *testing.T, tool mcpserver.Tool, want map[string]any) {
	t.Helper()
	if !reflect.DeepEqual(tool.Annotations, want) {
		t.Fatalf("%s annotations = %#v, want %#v", tool.Name, tool.Annotations, want)
	}
}

func TestAIDocumentProjectionSchemaCoversRecursiveCompactWire(t *testing.T) {
	decodeDefs := func(raw string) map[string]json.RawMessage {
		t.Helper()
		var schema struct {
			Definitions map[string]json.RawMessage `json:"$defs"`
		}
		if err := json.Unmarshal([]byte(raw), &schema); err != nil {
			t.Fatalf("decode schema: %v", err)
		}
		return schema.Definitions
	}
	decodeVariants := func(raw json.RawMessage) []map[string]any {
		t.Helper()
		var definition struct {
			OneOf []map[string]any `json:"oneOf"`
		}
		if err := json.Unmarshal(raw, &definition); err != nil {
			t.Fatalf("decode schema definition: %v", err)
		}
		return definition.OneOf
	}
	hasKind := func(variants []map[string]any, want string) bool {
		for _, variant := range variants {
			prefix, _ := variant["prefixItems"].([]any)
			if len(prefix) == 0 {
				continue
			}
			first, _ := prefix[0].(map[string]any)
			if first["const"] == want {
				return true
			}
			values, _ := first["enum"].([]any)
			for _, value := range values {
				if value == want {
					return true
				}
			}
		}
		return false
	}

	output := decodeDefs(projectionOutputJSONSchema)
	for _, kind := range []string{"l", "o"} {
		if !hasKind(decodeVariants(output["value"]), kind) {
			t.Fatalf("projection value schema omitted recursive kind %q", kind)
		}
	}
	fileVariants := decodeVariants(output["file"])
	if len(fileVariants) != 2 || fileVariants[0]["maxItems"] != float64(2) || fileVariants[1]["maxItems"] != float64(3) {
		t.Fatalf("projection File schema does not expose scalar and typed-path forms: %s", output["file"])
	}
}

func TestParagraphCreateSchemaRequiresPageRichTextParent(t *testing.T) {
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		AllOf      []struct {
			If struct {
				Properties map[string]struct {
					Const string `json:"const"`
				} `json:"properties"`
			} `json:"if"`
			Then struct {
				Required []string `json:"required"`
			} `json:"then"`
		} `json:"allOf"`
	}
	if err := json.Unmarshal([]byte(paragraphCreateInputJSONSchema), &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Properties["parent_block_id"] == nil || len(schema.AllOf) != 1 ||
		schema.AllOf[0].If.Properties["document_type"].Const != "page" ||
		!reflect.DeepEqual(schema.AllOf[0].Then.Required, []string{"parent_block_id"}) {
		t.Fatalf("Page paragraph parent contract = %#v", schema)
	}
}

func TestMutationSchemaDescribesEveryCompactOperationTuple(t *testing.T) {
	var schema struct {
		Definitions map[string]struct {
			Description string `json:"description"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal([]byte(mutationInputJSONSchema), &schema); err != nil {
		t.Fatal(err)
	}
	operation := schema.Definitions["operation"].Description
	for _, tuple := range []string{
		`["fs",fieldTarget,typedValue]`, `["fu",fieldTarget]`,
		`["bi",newBlockHandle,blockKind,parentBlockHandle,afterBlockHandle]`, `["bd",blockHandle]`,
		`["bm",blockHandle,parentBlockHandle,afterBlockHandle]`, `["bk",blockHandle,newBlockKind]`,
		`["ri",blockHandle,relationHandle,newItemHandle,itemKind,afterItemHandle]`,
		`["rd",blockHandle,relationHandle,itemHandle]`,
		`["rm",sourceBlockHandle,sourceRelationHandle,itemHandle,targetBlockHandle,targetRelationHandle,afterItemHandle]`,
		`["fa",fieldTarget,fileHandle]`, `["fd",fieldTarget]`, `["lc"]`, `["ld"]`,
		`["i",[["t",text]]]`,
	} {
		if !strings.Contains(operation, tuple) {
			t.Errorf("operation description omitted %s", tuple)
		}
	}
	if !strings.Contains(operation, "server decoder validates") || !strings.Contains(operation, "only operation in its batch") {
		t.Fatalf("operation validation/lifecycle instructions missing: %s", operation)
	}
	payload := schema.Definitions["compactPayload"].Description
	for _, syntax := range []string{
		`[block,"","",field]`, `[block,relation,item,field]`, `optional fifth item`,
		`["f",field]`, `["i",stableItemHandle]`,
		`["t",text]`, `["b",boolean]`, `["n",canonical-number-string]`, `["i",inline-items]`,
		`["l",[[optionalStableItemHandle,typedValue],...]]`, `["o",[[field,typedValue],...]]`,
		`b/em/u/s/code`, `fg/bg`, `["a",URL,inline-items]`, `["br"]`, `["math",expression]`, `["ph",placeholderHandle]`,
	} {
		if !strings.Contains(payload, syntax) {
			t.Errorf("compact payload description omitted %s", syntax)
		}
	}
}

func TestMutationSchemaAdvertisesRecursiveHomogeneousArrays(t *testing.T) {
	var schema struct {
		Properties map[string]struct {
			Items struct {
				Ref string `json:"$ref"`
			} `json:"items"`
		} `json:"properties"`
		Definitions map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal([]byte(mutationInputJSONSchema), &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.Definitions) != 2 || schema.Properties["o"].Items.Ref != "#/$defs/operation" {
		t.Fatal("mutation operations must use only the operation and compactPayload definitions")
	}
	var operation struct {
		Type     string `json:"type"`
		MinItems int    `json:"minItems"`
		MaxItems int    `json:"maxItems"`
		Items    struct {
			Ref string `json:"$ref"`
		} `json:"items"`
	}
	if err := json.Unmarshal(schema.Definitions["operation"], &operation); err != nil {
		t.Fatal(err)
	}
	if operation.Type != "array" || operation.MinItems != 1 || operation.MaxItems != 7 || operation.Items.Ref != "#/$defs/compactPayload" {
		t.Fatalf("operation array contract = %+v", operation)
	}
	var payload struct {
		AnyOf []struct {
			Type  string `json:"type"`
			Items struct {
				Ref string `json:"$ref"`
			} `json:"items"`
		} `json:"anyOf"`
	}
	if err := json.Unmarshal(schema.Definitions["compactPayload"], &payload); err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, branch := range payload.AnyOf {
		types = append(types, branch.Type)
		if branch.Type == "array" && branch.Items.Ref != "#/$defs/compactPayload" {
			t.Fatal("nested compact arrays must recursively preserve the same payload types")
		}
	}
	if !reflect.DeepEqual(types, []string{"string", "boolean", "array"}) {
		t.Fatalf("compact payload must have disjoint string, boolean, and array alternatives, got %v", types)
	}
	for _, definition := range schema.Definitions {
		for _, unsupported := range []string{`"prefixItems":`, `"items":false`, `"oneOf":`, `"const":`, `"enum":`} {
			if strings.Contains(string(definition), unsupported) {
				t.Fatalf("compact input definition retained tuple projection constraints: %s", definition)
			}
		}
	}
}

func TestCompactOperationsReachApplyThroughHTTPWhileMalformedTuplesDoNot(t *testing.T) {
	const documentID = "0c314c79-103b-4e0e-953e-5f9370851639"
	const block = "51e895a2-24c4-4aaa-a28c-fc466c5590fd"
	const parent = "0bb8af09-e358-488b-9950-c07b6d056ca5"
	const after = "8272c725-6186-4c0c-a244-3cb7025ca92e"
	for _, test := range []struct {
		name       string
		operations string
		want       []core.Operation
	}{
		{"valid move batch", `[["bm","` + block + `","` + parent + `","` + after + `"],["bm","` + after + `","` + parent + `",""],["bm","` + block + `","` + parent + `","` + after + `"]]`, []core.Operation{
			core.MoveBlockOperation(block, parent, after), core.MoveBlockOperation(after, parent, ""), core.MoveBlockOperation(block, parent, after),
		}},
		{"mixed recursive values", `[["fs",["` + block + `","","","enabled"],["b",true]],["fs",["` + block + `","","","width"],["n","12.5"]],["fs",["` + block + `","","","metadata"],["o",[["nested",["l",[["",["i",[["b",[["t","text"]]]]]]]]]]]]]`, []core.Operation{
			core.SetFieldOperation(block, "enabled", core.Boolean(true)),
			core.SetFieldOperation(block, "width", core.Number("12.5")),
			core.SetFieldOperation(block, "metadata", core.Object(core.ObjectValue("nested", core.List(core.PositionalItem(core.RichText(core.Bold(core.InlineText("text")))))))),
		}},
		{"missing predecessor", `[["bm","` + block + `","` + parent + `"]]`, nil},
		{"extra position", `[["bm","` + block + `","` + parent + `","` + after + `","extra"]]`, nil},
		{"non-string parent", `[["bm","` + block + `",false,"` + after + `"]]`, nil},
		{"null predecessor", `[["bm","` + block + `","` + parent + `",null]]`, nil},
		{"unknown tag", `[["move","` + block + `","` + parent + `","` + after + `"]]`, nil},
		{"native numeric payload", `[["fs",["` + block + `","","","width"],["n",12.5]]]`, nil},
		{"object payload", `[["fs",["` + block + `","","","metadata"],{"t":"text"}]]`, nil},
		{"null inline payload", `[["fs",["` + block + `","","","content"],["i",[["t",null]]]]]`, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			application := &recordingAIDocumentApplication{applyResult: core.ApplyResult{DocumentRevision: "revision-b", Normalized: test.want}}
			tools := mustAIDocumentTools(t, application)
			config := validHTTPConfig(nil)
			config.Registry, config.Dispatcher = tools, tools
			response := httptest.NewRecorder()
			request := mcpHTTPRequest(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"document_apply","arguments":{"v":"dcdp/1","p":"page","d":"` + documentID + `","l":"en","edr":"0af8a257-9da1-4a5d-9ae3-22682b3b4219","o":` + test.operations + `}}}`)
			newHTTPTestHandler(t, config).ServeHTTP(response, request)
			var reply struct {
				Result struct {
					IsError bool `json:"isError"`
				} `json:"result"`
				Error json.RawMessage `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &reply); err != nil {
				t.Fatal(err)
			}
			if response.Code != 200 || reply.Error != nil || reply.Result.IsError != (test.want == nil) {
				t.Fatalf("HTTP result = %d %s", response.Code, response.Body.String())
			}
			if test.want == nil {
				if application.applyCalls != 0 {
					t.Fatal("malformed tuple reached the application")
				}
			} else if application.applyCalls != 1 || !reflect.DeepEqual(application.applyRequest.Operations, test.want) {
				t.Fatalf("decoded compact batch = %+v", application.applyRequest)
			}
		})
	}
}

func TestAIDocumentToolsServeListAndCallThroughAuthenticatedHTTPAssembly(t *testing.T) {
	application := &recordingAIDocumentApplication{openResult: core.OpenMetadata{
		Protocol: core.ProtocolVersion, Profile: core.DomainPage, Catalog: "catalog-page",
		Document: "55555555-5555-4555-8555-555555555555", DocumentRevision: "revision-a", SourceLocale: "ko", Locale: "ko",
		LocaleRole: core.LocaleRoleSource, LocaleExists: true,
	}}
	tools := mustAIDocumentTools(t, application)
	config := validHTTPConfig(nil)
	config.Registry = tools
	config.Dispatcher = tools
	handler := newHTTPTestHandler(t, config)

	listRequest := mcpHTTPRequest(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, listRequest)
	if listResponse.Code != 200 {
		t.Fatalf("tools/list = %d %s", listResponse.Code, listResponse.Body.String())
	}
	for _, name := range []string{
		ToolDocumentOpen, ToolDocumentRead,
		ToolParagraphCreate, ToolParagraphUpdate, ToolBlockDelete,
		ToolDocumentValidate, ToolDocumentApply,
	} {
		if !strings.Contains(listResponse.Body.String(), `"name":"`+name+`"`) {
			t.Fatalf("tools/list omitted %q: %s", name, listResponse.Body.String())
		}
	}

	callRequest := mcpHTTPRequest(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"document_open","arguments":{"p":"page","d":"55555555-5555-4555-8555-555555555555","l":"ko"}}}`)
	callResponse := httptest.NewRecorder()
	handler.ServeHTTP(callResponse, callRequest)
	if callResponse.Code != 200 || !strings.Contains(callResponse.Body.String(), `"structuredContent":{"c":"catalog-page"`) {
		t.Fatalf("tools/call = %d %s", callResponse.Code, callResponse.Body.String())
	}
}

func TestFocusedDocumentToolsTranslatePlainParagraphActionsToTypedApply(t *testing.T) {
	const (
		documentID = "44444444-4444-4444-8444-444444444444"
		revision   = "revision-a"
	)

	t.Run("create", func(t *testing.T) {
		application := &recordingAIDocumentApplication{applyResult: core.ApplyResult{
			DocumentRevision: "revision-b",
			Changes: []core.Change{
				{Operation: 0, Kind: core.OperationInsertBlock},
				{Operation: 1, Kind: core.OperationSetField},
			},
		}}
		result, err := mustAIDocumentTools(t, application).CallTool(
			t.Context(), mcpserver.Principal{}, ToolParagraphCreate,
			toolArguments(t, `{"document_type":"post","document_id":"`+documentID+`","locale":"ko","expected_document_revision":"`+revision+`","after_block_id":"paragraph-a","text":"새 문단"}`),
		)
		if err != nil || result.IsError {
			t.Fatalf("document_paragraph_create = %+v, %v", result, err)
		}
		blockID := stringValue(t, result.StructuredContent, "block_id")
		if _, err := uuid.Parse(blockID); err != nil {
			t.Fatalf("created block_id = %q, want UUID: %v", blockID, err)
		}
		want := []core.Operation{
			core.InsertBlockOperation(core.BlockID(blockID), "paragraph", "", "paragraph-a"),
			core.SetFieldOperation(core.BlockID(blockID), "content", core.RichText(core.InlineText("새 문단"))),
		}
		if !reflect.DeepEqual(application.applyRequest.Operations, want) {
			t.Fatalf("create operations = %+v, want %+v", application.applyRequest.Operations, want)
		}
	})

	t.Run("create inside Page rich-text section", func(t *testing.T) {
		application := &recordingAIDocumentApplication{applyResult: core.ApplyResult{
			DocumentRevision: "revision-b",
			Changes: []core.Change{
				{Operation: 0, Kind: core.OperationInsertBlock},
				{Operation: 1, Kind: core.OperationSetField},
			},
		}}
		result, err := mustAIDocumentTools(t, application).CallTool(
			t.Context(), mcpserver.Principal{}, ToolParagraphCreate,
			toolArguments(t, `{"document_type":"page","document_id":"`+documentID+`","locale":"ko","expected_document_revision":"`+revision+`","parent_block_id":"section-a","after_block_id":"paragraph-a","text":"Page 문단"}`),
		)
		if err != nil || result.IsError {
			t.Fatalf("Page document_paragraph_create = %+v, %v", result, err)
		}
		blockID := stringValue(t, result.StructuredContent, "block_id")
		want := []core.Operation{
			core.InsertBlockOperation(core.BlockID(blockID), "paragraph", "section-a", "paragraph-a"),
			core.SetFieldOperation(core.BlockID(blockID), "content", core.RichText(core.InlineText("Page 문단"))),
		}
		if !reflect.DeepEqual(application.applyRequest.Operations, want) {
			t.Fatalf("Page create operations = %+v, want %+v", application.applyRequest.Operations, want)
		}
	})

	t.Run("update", func(t *testing.T) {
		application := &recordingAIDocumentApplication{applyResult: core.ApplyResult{
			DocumentRevision: "revision-b",
			Changes:          []core.Change{{Operation: 0, Kind: core.OperationSetField}},
		}}
		result, err := mustAIDocumentTools(t, application).CallTool(
			t.Context(), mcpserver.Principal{}, ToolParagraphUpdate,
			toolArguments(t, `{"document_type":"post","document_id":"`+documentID+`","locale":"ko","expected_document_revision":"`+revision+`","block_id":"paragraph-a","text":"수정 문단"}`),
		)
		if err != nil || result.IsError {
			t.Fatalf("document_paragraph_update = %+v, %v", result, err)
		}
		want := []core.Operation{core.SetFieldOperation("paragraph-a", "content", core.RichText(core.InlineText("수정 문단")))}
		if !reflect.DeepEqual(application.applyRequest.Operations, want) {
			t.Fatalf("update operations = %+v, want %+v", application.applyRequest.Operations, want)
		}
	})

	t.Run("delete", func(t *testing.T) {
		application := &recordingAIDocumentApplication{applyResult: core.ApplyResult{
			DocumentRevision: "revision-b",
			Changes:          []core.Change{{Operation: 0, Kind: core.OperationDeleteBlock}},
		}}
		result, err := mustAIDocumentTools(t, application).CallTool(
			t.Context(), mcpserver.Principal{}, ToolBlockDelete,
			toolArguments(t, `{"document_type":"post","document_id":"`+documentID+`","locale":"ko","expected_document_revision":"`+revision+`","block_id":"paragraph-a"}`),
		)
		if err != nil || result.IsError {
			t.Fatalf("document_block_delete = %+v, %v", result, err)
		}
		want := []core.Operation{core.DeleteBlockOperation("paragraph-a")}
		if !reflect.DeepEqual(application.applyRequest.Operations, want) {
			t.Fatalf("delete operations = %+v, want %+v", application.applyRequest.Operations, want)
		}
	})
}

func TestFocusedParagraphTextRequiresPresenceAndPreservesExplicitEmpty(t *testing.T) {
	for _, tool := range []string{ToolParagraphCreate, ToolParagraphUpdate} {
		for _, test := range []struct {
			name  string
			text  string
			valid bool
		}{
			{"missing", "", false},
			{"null", `,"text":null`, false},
			{"explicit empty", `,"text":""`, true},
		} {
			t.Run(tool+"/"+test.name, func(t *testing.T) {
				application := &recordingAIDocumentApplication{applyResult: core.ApplyResult{DocumentRevision: "revision-b"}}
				arguments := `{"document_type":"post","document_id":"44444444-4444-4444-8444-444444444444","locale":"ko","expected_document_revision":"revision-a"`
				if tool == ToolParagraphUpdate {
					arguments += `,"block_id":"paragraph-a"`
				}
				arguments += test.text + `}`
				_, err := mustAIDocumentTools(t, application).CallTool(t.Context(), mcpserver.Principal{}, tool, toolArguments(t, arguments))
				if !test.valid {
					var execution *mcpserver.ToolExecutionError
					if !errors.As(err, &execution) || application.applyCalls != 0 {
						t.Fatalf("invalid required text reached Apply: calls=%d, err=%v", application.applyCalls, err)
					}
					return
				}
				if err != nil || application.applyCalls != 1 {
					t.Fatalf("explicit empty text rejected: calls=%d, err=%v", application.applyCalls, err)
				}
				operation := application.applyRequest.Operations[len(application.applyRequest.Operations)-1]
				if !reflect.DeepEqual(operation.SetField.Value, core.RichText(core.InlineText(""))) {
					t.Fatalf("explicit empty replacement=%+v", operation.SetField.Value)
				}
			})
		}
	}
}

func TestAIDocumentToolsOpenAndRead(t *testing.T) {
	targetRevision := core.Revision("target-revision-a")
	application := &recordingAIDocumentApplication{
		openResult: core.OpenMetadata{
			Protocol: core.ProtocolVersion, Profile: core.DomainPost, Catalog: "catalog-a",
			Document: "44444444-4444-4444-8444-444444444444", DocumentRevision: "revision-a", TargetRevision: &targetRevision, SourceLocale: "ko", Locale: "en",
			LocaleRole: core.LocaleRoleNonSource, LocaleExists: true,
		},
		readResult: core.Projection{
			Protocol: core.ProtocolVersion, Profile: core.DomainPost, Catalog: "catalog-a",
			Document: "44444444-4444-4444-8444-444444444444", DocumentRevision: "revision-a", TargetRevision: &targetRevision, SourceLocale: "ko", Locale: "en",
			LocaleRole: core.LocaleRoleNonSource, LocaleExists: true, Mode: core.ReadFields,
			Nodes: []core.Node{{
				ID: "paragraph-a", Kind: "paragraph",
				Localized: []core.FieldValue{{ID: "content", Value: core.Text("")}},
				Relations: []core.Relation{{ID: "credits", Items: []core.RelationItem{{ID: "credit-main", Kind: "artist", Localized: []core.FieldValue{{ID: "name", Value: core.Text("Artist")}}}}}},
			}},
		},
	}
	tools := mustAIDocumentTools(t, application)

	opened, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentOpen, toolArguments(t, `{"p":"post","d":"44444444-4444-4444-8444-444444444444","l":"en"}`))
	if err != nil {
		t.Fatalf("document_open error = %v", err)
	}
	if opened.IsError || stringValue(t, opened.StructuredContent, "v") != core.ProtocolVersion || strings.Contains(opened.Content[0]["text"].(string), "profile") {
		t.Fatalf("document_open result is not compact DCDP/1: %+v", opened)
	}
	wantOpen := core.OpenRequest{Document: core.DocumentIdentity{Domain: core.DomainPost, Reference: "44444444-4444-4444-8444-444444444444"}, Locale: "en"}
	if !reflect.DeepEqual(application.openRequest, wantOpen) {
		t.Fatalf("Open request = %+v, want %+v", application.openRequest, wantOpen)
	}

	read, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentRead, toolArguments(t, `{
		"p":"post","d":"44444444-4444-4444-8444-444444444444","l":"en","m":"fields",
		"f":[["paragraph-a","content"],["paragraph-a","credits","credit-main","name"]],"n":12,"c":"cursor-a"
	}`))
	if err != nil {
		t.Fatalf("document_read error = %v", err)
	}
	wantFields := []core.FieldSelection{
		{Block: "paragraph-a", Field: "content"},
		{Block: "paragraph-a", Relation: "credits", Item: "credit-main", Field: "name"},
	}
	if application.readRequest.Mode != core.ReadFields || application.readRequest.Limit != 12 || application.readRequest.Cursor != "cursor-a" || !reflect.DeepEqual(application.readRequest.Fields, wantFields) {
		t.Fatalf("Read request lost compact selectors: %+v", application.readRequest)
	}
	text := read.Content[0]["text"].(string)
	for _, forbidden := range []string{"tiptap", "prosemirror", "yjs", "<p>"} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Fatalf("document_read exposed %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, `["content",["t",""]]`) {
		t.Fatalf("document_read did not preserve explicit empty: %s", text)
	}
}

func TestAIDocumentToolsRejectSlugAndReturnExpectedApplicationErrorsAsToolResults(t *testing.T) {
	application := &recordingAIDocumentApplication{}
	tools := mustAIDocumentTools(t, application)

	result, err := tools.CallTool(
		t.Context(),
		mcpserver.Principal{},
		ToolDocumentOpen,
		toolArguments(t, `{"p":"post","d":"test-post","l":"ko"}`),
	)
	var executionErr *mcpserver.ToolExecutionError
	if result.Content != nil || !errors.As(err, &executionErr) || executionErr.Message != "d must be a canonical UUID" {
		t.Fatalf("slug document_open result = %#v, error = %v", result, err)
	}
	if application.openRequest.Document.Reference != "" {
		t.Fatalf("slug document_open reached application: %#v", application.openRequest)
	}

	application.openError = connect.NewError(connect.CodeNotFound, errors.New("post not found"))
	result, err = tools.CallTool(
		t.Context(),
		mcpserver.Principal{},
		ToolDocumentOpen,
		toolArguments(t, `{"p":"post","d":"44444444-4444-4444-8444-444444444444","l":"ko"}`),
	)
	if result.Content != nil || !errors.As(err, &executionErr) || executionErr.Message != "post not found" {
		t.Fatalf("not-found document_open result = %#v, error = %v", result, err)
	}
}

func TestAIDocumentToolsValidateAndApply(t *testing.T) {
	operation := core.SetFieldOperation("paragraph-a", "content", core.RichText(core.InlineText("Hello"), core.Bold(core.InlineText("world"))))
	targetRevision := core.Revision("target-revision-a")
	nextTargetRevision := core.Revision("target-revision-b")
	application := &recordingAIDocumentApplication{
		validateResult: core.ValidationResult{Normalized: []core.Operation{operation}},
		applyResult: core.ApplyResult{DocumentRevision: "revision-a", TargetRevision: &nextTargetRevision, Changes: []core.Change{{
			Operation: 0, Kind: core.OperationSetField,
			AffectedHandles: []string{"field:paragraph-a/content", "translation:en"},
		}}},
	}
	tools := mustAIDocumentTools(t, application)
	encodedRequest, err := core.EncodeApplyRequest(core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainPost, Document: "44444444-4444-4444-8444-444444444444",
		Locale: "en", ExpectedDocumentRevision: "revision-a", ExpectedTargetRevision: &targetRevision, Operations: []core.Operation{operation},
	})
	if err != nil {
		t.Fatalf("EncodeApplyRequest() error = %v", err)
	}
	arguments := toolArguments(t, string(encodedRequest))

	validated, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentValidate, arguments)
	if err != nil {
		t.Fatalf("document_validate error = %v", err)
	}
	if validated.IsError || !strings.Contains(validated.Content[0]["text"].(string), `"o":[["fs"`) {
		t.Fatalf("document_validate did not return normalized compact operations: %+v", validated)
	}

	applied, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentApply, arguments)
	if err != nil {
		t.Fatalf("document_apply error = %v", err)
	}
	if applied.IsError || applied.Content[0]["text"] != `{"c":[[0,"fs",["field:paragraph-a/content","translation:en"]]],"dr":"revision-a","tr":"target-revision-b"}` {
		t.Fatalf("document_apply result = %+v", applied)
	}
	if !reflect.DeepEqual(application.applyRequest.Operations, []core.Operation{operation}) {
		t.Fatalf("Apply request lost typed operation: %+v", application.applyRequest)
	}
}

func TestAIDocumentToolsReturnsTypedMutationRejections(t *testing.T) {
	operation := core.SetFieldOperation("paragraph-a", "content", core.Text("Hello"))
	targetRevision := core.Revision("target-revision-a")
	request := core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainPost, Document: "44444444-4444-4444-8444-444444444444",
		Locale: "en", ExpectedDocumentRevision: "revision-a", ExpectedTargetRevision: &targetRevision, Operations: []core.Operation{operation},
	}
	encoded, err := core.EncodeApplyRequest(request)
	if err != nil {
		t.Fatalf("EncodeApplyRequest() error = %v", err)
	}

	tests := []struct {
		name        string
		application *recordingAIDocumentApplication
		want        string
	}{
		{
			name: "validation",
			application: &recordingAIDocumentApplication{applyError: &core.ValidationError{Result: core.ValidationResult{
				Normalized: []core.Operation{operation},
				Issues:     []core.OperationIssue{{Operation: 0, Code: core.IssueTargetFieldForbidden, Handle: "field:paragraph-a/content", Message: "target locale cannot mutate shared field"}},
			}}},
			want: `"i":[[0,"target_field_forbidden","field:paragraph-a/content","target locale cannot mutate shared field"]]`,
		},
		{
			name: "document conflict",
			application: &recordingAIDocumentApplication{applyError: &core.ConflictError{Conflict: core.Conflict{
				Code: core.ConflictDocumentRevision, CurrentDocumentRevision: "revision-new", AffectedHandles: []string{"paragraph-a"},
			}}},
			want: `"reason":"document_revision_changed"`,
		},
		{
			name: "target conflict",
			application: &recordingAIDocumentApplication{applyError: &core.ConflictError{Conflict: core.Conflict{
				Code: core.ConflictTargetRevision, CurrentDocumentRevision: "revision-new", CurrentTargetRevision: &targetRevision, AffectedHandles: []string{"paragraph-a"},
			}}},
			want: `"reason":"target_revision_changed"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tools := mustAIDocumentTools(t, test.application)
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentApply, toolArguments(t, string(encoded)))
			if err != nil {
				t.Fatalf("document_apply error = %v", err)
			}
			if result.IsError != (test.name == "validation") || !strings.Contains(result.Content[0]["text"].(string), test.want) {
				t.Fatalf("document_apply rejection = %+v, want %s", result, test.want)
			}
		})
	}
}

func TestAIDocumentToolsRejectsMalformedOrEditorNativeArguments(t *testing.T) {
	tools := mustAIDocumentTools(t, &recordingAIDocumentApplication{})
	tests := []struct {
		name      string
		tool      string
		arguments string
	}{
		{name: "unknown open field", tool: ToolDocumentOpen, arguments: `{"p":"post","d":"44444444-4444-4444-8444-444444444444","l":"en","html":"<p>x</p>"}`},
		{name: "positional field selector", tool: ToolDocumentRead, arguments: `{"p":"post","d":"44444444-4444-4444-8444-444444444444","l":"en","m":"fields","f":[["paragraph-a","content","extra"]]}`},
		{name: "generic JSON patch", tool: ToolDocumentApply, arguments: `{"v":"dcdp/1","p":"post","d":"44444444-4444-4444-8444-444444444444","l":"en","edr":"revision-a","o":[["json_patch","/nodes/0",{}]]}`},
		{name: "raw yjs", tool: ToolDocumentApply, arguments: `{"v":"dcdp/1","p":"post","d":"44444444-4444-4444-8444-444444444444","l":"en","edr":"revision-a","o":[["yjs","AAAA"]]}`},
		{name: "raw html", tool: ToolDocumentApply, arguments: `{"v":"dcdp/1","p":"post","d":"44444444-4444-4444-8444-444444444444","l":"en","edr":"revision-a","o":[["fs",["paragraph-a","","","content"],["html","<p>x</p>"]]]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, test.tool, toolArguments(t, test.arguments))
			var execution *mcpserver.ToolExecutionError
			if !errors.As(err, &execution) || result.Content != nil {
				t.Fatalf("CallTool() = %+v, %v; want safe execution error", result, err)
			}
		})
	}

	if _, err := tools.CallTool(t.Context(), mcpserver.Principal{}, "missing", mcpserver.ToolArguments{}); !errors.Is(err, mcpserver.ErrUnknownTool) {
		t.Fatalf("unknown tool error = %v", err)
	}
	if _, err := NewAIDocumentTools(nil); err == nil {
		t.Fatal("NewAIDocumentTools(nil) succeeded")
	}
}

func TestAIDocumentToolsFailClosedOnInvalidApplicationResults(t *testing.T) {
	tests := []struct {
		name        string
		application *recordingAIDocumentApplication
		tool        string
	}{
		{name: "invalid metadata", application: &recordingAIDocumentApplication{openResult: core.OpenMetadata{}}, tool: ToolDocumentOpen},
		{name: "negative validation operation", application: &recordingAIDocumentApplication{validateResult: core.ValidationResult{Issues: []core.OperationIssue{{Operation: -1, Code: core.IssueInvalidOperation}}}}, tool: ToolDocumentValidate},
		{name: "empty accepted revision", application: &recordingAIDocumentApplication{applyResult: core.ApplyResult{}}, tool: ToolDocumentApply},
	}
	operation := core.SetFieldOperation("paragraph-a", "content", core.Text("x"))
	mutation, err := core.EncodeApplyRequest(core.ApplyRequest{Protocol: core.ProtocolVersion, Profile: core.DomainPost, Document: "44444444-4444-4444-8444-444444444444", Locale: "en", ExpectedDocumentRevision: "revision-a", Operations: []core.Operation{operation}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			arguments := toolArguments(t, string(mutation))
			if test.tool == ToolDocumentOpen {
				arguments = toolArguments(t, `{"p":"post","d":"44444444-4444-4444-8444-444444444444","l":"en"}`)
			}
			result, err := mustAIDocumentTools(t, test.application).CallTool(t.Context(), mcpserver.Principal{}, test.tool, arguments)
			if err == nil || result.Content != nil {
				t.Fatalf("CallTool() = %+v, %v; want internal error", result, err)
			}
			var execution *mcpserver.ToolExecutionError
			if errors.As(err, &execution) {
				t.Fatalf("invalid application result became client-visible error: %v", err)
			}
		})
	}
}

type recordingAIDocumentApplication struct {
	openRequest     core.OpenRequest
	readRequest     core.ReadRequest
	validateRequest core.ApplyRequest
	applyRequest    core.ApplyRequest
	openResult      core.OpenMetadata
	readResult      core.Projection
	validateResult  core.ValidationResult
	applyResult     core.ApplyResult
	openError       error
	readError       error
	validateError   error
	applyError      error
	applyCalls      int
	readCalls       int
}

func (application *recordingAIDocumentApplication) Open(_ context.Context, request core.OpenRequest) (core.OpenMetadata, error) {
	application.openRequest = request
	return application.openResult, application.openError
}

func (application *recordingAIDocumentApplication) Read(_ context.Context, request core.ReadRequest) (core.Projection, error) {
	application.readCalls++
	application.readRequest = request
	return application.readResult, application.readError
}

func (application *recordingAIDocumentApplication) Validate(_ context.Context, request core.ApplyRequest) (core.ValidationResult, error) {
	application.validateRequest = request
	return application.validateResult, application.validateError
}

func (application *recordingAIDocumentApplication) Apply(_ context.Context, request core.ApplyRequest) (core.ApplyResult, error) {
	application.applyCalls++
	application.applyRequest = request
	return application.applyResult, application.applyError
}

func mustAIDocumentTools(t *testing.T, application *recordingAIDocumentApplication) *AIDocumentTools {
	t.Helper()
	tools, err := NewAIDocumentTools(application)
	if err != nil {
		t.Fatalf("NewAIDocumentTools() error = %v", err)
	}
	return tools
}

func toolArguments(t *testing.T, value string) mcpserver.ToolArguments {
	t.Helper()
	var arguments mcpserver.ToolArguments
	if err := json.Unmarshal([]byte(value), &arguments); err != nil {
		t.Fatalf("decode tool arguments: %v", err)
	}
	return arguments
}

func stringValue(t *testing.T, values map[string]any, key string) string {
	t.Helper()
	value, ok := values[key].(string)
	if !ok {
		t.Fatalf("structured content %q = %T, want string", key, values[key])
	}
	return value
}

func TestDocumentMetadataUpdateSupportsProgramEventExactRevisions(t *testing.T) {
	application := &recordingAIDocumentApplication{applyResult: core.ApplyResult{DocumentRevision: "revision-a", Changed: true, TargetRevision: revisionPointer("target-b"), Changes: []core.Change{{Operation: 0, Kind: core.OperationSetField, AffectedHandles: []string{"document:summary"}}}}}
	result, err := mustAIDocumentTools(t, application).CallTool(t.Context(), mcpserver.Principal{}, ToolMetadataUpdate, toolArguments(t, `{"document_type":"program_event","document_id":"44444444-4444-4444-8444-444444444444","locale":"ko","expected_document_revision":"revision-a","expected_target_revision":"target-a","summary":"번역 요약"}`))
	if err != nil || result.IsError {
		t.Fatalf("Program Event metadata update = %+v, %v", result, err)
	}
	request := application.applyRequest
	if request.Profile != core.DomainProgramEvent || request.Locale != "ko" || request.ExpectedTargetRevision == nil || *request.ExpectedTargetRevision != "target-a" {
		t.Fatalf("metadata identity/CAS = %+v", request)
	}
	if len(request.Operations) != 1 || request.Operations[0].SetField.Target.Block != "document" || request.Operations[0].SetField.Target.Field != "summary" {
		t.Fatalf("metadata operations = %+v", request.Operations)
	}
	var schema struct {
		Properties struct {
			DocumentType struct {
				Enum []string `json:"enum"`
			} `json:"document_type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(documentMetadataUpdateInputJSONSchema), &schema); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, domain := range schema.Properties.DocumentType.Enum {
		if domain == "program_event" {
			found = true
		}
	}
	if !found {
		t.Fatal("Program Event excluded from advertised metadata input schema")
	}
	application.applyCalls = 0
	result, err = mustAIDocumentTools(t, application).CallTool(t.Context(), mcpserver.Principal{}, ToolMetadataUpdate, toolArguments(t, `{"document_type":"program_event","document_id":"44444444-4444-4444-8444-444444444444","locale":"en","expected_document_revision":"revision-a","title":"Event title","tag_ids":[]}`))
	var execution *mcpserver.ToolExecutionError
	if !errors.As(err, &execution) || application.applyCalls != 0 {
		t.Fatalf("Program Event accepted Post-only metadata: %+v %v calls=%d", result, err, application.applyCalls)
	}
}
