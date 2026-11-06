package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect"
	core "github.com/echovisionlab/geul-api/internal/aidocument"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/google/uuid"
)

func TestAIDocumentToolsListTypedSurface(t *testing.T) {
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
		ToolBlockCreate, ToolBlockUpdate, ToolDocumentRead, ToolBlockDelete, ToolMetadataUpdate, ToolDocumentApply,
	}
	wantAnnotations := map[string]map[string]any{
		ToolDocumentRead:   toolAnnotations(true, false, false),
		ToolBlockCreate:    toolAnnotations(false, false, false),
		ToolBlockUpdate:    toolAnnotations(false, true, false),
		ToolBlockDelete:    toolAnnotations(false, true, false),
		ToolMetadataUpdate: toolAnnotations(false, true, false),
		ToolDocumentApply:  toolAnnotations(false, true, false),
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
	listed[0].Annotations["readOnlyHint"] = true
	again, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	if err != nil {
		t.Fatalf("second ListTools() error = %v", err)
	}
	if again[0].InputSchema[0] != '{' || again[0].SecuritySchemes[0].Scopes[0] != "mcp" ||
		again[0].Meta["securitySchemes"].([]mcpserver.ToolSecurityScheme)[0].Scopes[0] != "mcp" ||
		again[0].Annotations["readOnlyHint"] != false {
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
	if result.StructuredContent["document_revision"] != "revision-b" {
		t.Fatalf("document_metadata_update result = %#v", result.StructuredContent)
	}
	change := result.StructuredContent["changes"].([]any)[0].(map[string]any)
	if change["op"] != string(core.OperationUnsetField) {
		t.Fatalf("clear summary change=%v", change)
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
	for _, name := range []string{ToolParagraphCreate, ToolParagraphUpdate, ToolHeadingCreate, ToolHeadingUpdate, ToolTableCreate, ToolBlockDelete, ToolMetadataUpdate, ToolDocumentApply} {
		t.Run(name, func(t *testing.T) {
			application := &recordingAIDocumentApplication{applyError: &core.ValidationError{Result: core.ValidationResult{
				Normalized: []core.Operation{operation},
				Issues:     []core.OperationIssue{{Operation: 0, Code: core.IssueValueKindMismatch, Handle: "field:paragraph-a/content", Message: "wrong value kind"}},
			}}}
			arguments := toolArguments(t, `{"document_type":"post","document_id":"44444444-4444-4444-8444-444444444444","locale":"ko","expected_document_revision":"revision-a"}`)
			switch name {
			case ToolHeadingCreate, ToolHeadingUpdate:
				arguments["text"] = json.RawMessage(`"heading"`)
				arguments["level"] = json.RawMessage(`2`)
				if name == ToolHeadingUpdate {
					arguments["block_id"] = json.RawMessage(`"heading-a"`)
				}
			case ToolTableCreate:
				arguments["rows"] = json.RawMessage(`[["cell"]]`)
				arguments["after_block_id"] = json.RawMessage(`"paragraph-a"`)
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
				arguments = toolArguments(t, `{"document_type":"post","document_id":"44444444-4444-4444-8444-444444444444","locale":"ko","expected_document_revision":"revision-a","operations":[{"op":"set_field","block_id":"paragraph-a","field":"content","value":{"type":"text","text":"wrong kind"}}]}`)
			}
			tools := mustAIDocumentTools(t, application)
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, name, arguments)
			if err != nil || !result.IsError || result.StructuredContent["issues"] == nil || result.StructuredContent["operations"] != nil {
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
						if resultBranch.Properties["issues"] != nil && resultBranch.Properties["operations"] != nil {
							declared = true
						}
					}
				}
				if !declared {
					t.Fatalf("actual validation rejection excluded by %s output schema: %s", name, tool.OutputSchema)
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

func TestAIDocumentProjectionSchemaCoversEveryValueType(t *testing.T) {
	var schema struct {
		Definitions map[string]struct {
			AnyOf []struct {
				Properties struct {
					Type struct {
						Const string   `json:"const"`
						Enum  []string `json:"enum"`
					} `json:"type"`
				} `json:"properties"`
			} `json:"anyOf"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal([]byte(projectionOutputJSONSchema), &schema); err != nil {
		t.Fatal(err)
	}
	types := func(definition string) map[string]bool {
		found := map[string]bool{}
		for _, alternative := range schema.Definitions[definition].AnyOf {
			found[alternative.Properties.Type.Const] = true
			for _, name := range alternative.Properties.Type.Enum {
				found[name] = true
			}
		}
		return found
	}
	for _, kind := range []core.ValueKind{core.ValueKindText, core.ValueKindNumber, core.ValueKindBoolean, core.ValueKindInline, core.ValueKindList, core.ValueKindObject} {
		if !types("value")[string(kind)] {
			t.Errorf("value schema omits %s", kind)
		}
	}
	for _, kind := range []core.InlineKind{
		core.InlineKindText, core.InlineKindBold, core.InlineKindItalic, core.InlineKindUnderline, core.InlineKindStrike, core.InlineKindCode,
		core.InlineKindTextColor, core.InlineKindBackground, core.InlineKindLink, core.InlineKindHardBreak, core.InlineKindSiteVariable,
		core.InlineKindMath, core.InlineKindPlaceholder,
	} {
		if !types("inlineItem")[string(kind)] {
			t.Errorf("inline schema omits %s", kind)
		}
	}
}

// Codex lists a tool whose input schema has a top-level allOf/if-then as
// "args: unknown", so create tools describe the Page parent rule in text and
// the handlers enforce it instead.
func TestCreateSchemasAvoidTopLevelConditionals(t *testing.T) {
	for name, raw := range map[string]json.RawMessage{
		ToolBlockCreate:     json.RawMessage(blockCreateInputJSONSchema),
		ToolBlockUpdate:     json.RawMessage(blockUpdateInputJSONSchema),
		ToolDocumentFileAdd: json.RawMessage(documentFileAddInputJSONSchema),
	} {
		var schema map[string]json.RawMessage
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if schema["properties"] == nil {
			t.Fatalf("%s schema has no properties", name)
		}
		for _, keyword := range []string{"allOf", "anyOf", "oneOf", "if", "then", "not"} {
			if _, ok := schema[keyword]; ok {
				t.Errorf("%s schema has top-level %s", name, keyword)
			}
		}
	}
}

type mutationSchemaAlternative struct {
	Type                 string                     `json:"type"`
	AdditionalProperties *bool                      `json:"additionalProperties"`
	Required             []string                   `json:"required"`
	Properties           map[string]json.RawMessage `json:"properties"`
}

// Every operation the decoder accepts must be advertised with the fields it
// requires, so a caller can write one from the schema alone.
func TestMutationSchemaAdvertisesEveryOperationAsANamedObject(t *testing.T) {
	var schema struct {
		Properties map[string]struct {
			Items struct {
				Ref string `json:"$ref"`
			} `json:"items"`
		} `json:"properties"`
		Definitions map[string]struct {
			AnyOf []mutationSchemaAlternative `json:"anyOf"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal([]byte(mutationInputJSONSchema), &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Properties["operations"].Items.Ref != "#/$defs/operation" {
		t.Fatal("operations must reference the operation definition")
	}
	advertised := map[string][]string{}
	for _, alternative := range schema.Definitions["operation"].AnyOf {
		if alternative.Type != "object" || alternative.AdditionalProperties == nil || *alternative.AdditionalProperties {
			t.Fatalf("operation alternative must be a closed object: %+v", alternative)
		}
		var op struct {
			Const string   `json:"const"`
			Enum  []string `json:"enum"`
		}
		if err := json.Unmarshal(alternative.Properties["op"], &op); err != nil {
			t.Fatal(err)
		}
		for _, name := range append(op.Enum, op.Const) {
			if name != "" {
				advertised[name] = alternative.Required
			}
		}
	}
	for name, required := range map[core.OperationKind][]string{
		core.OperationSetField:           {"op", "block_id", "field", "value"},
		core.OperationUnsetField:         {"op", "block_id", "field"},
		core.OperationInsertBlock:        {"op", "block_id", "kind"},
		core.OperationDeleteBlock:        {"op", "block_id"},
		core.OperationMoveBlock:          {"op", "block_id"},
		core.OperationReplaceBlockKind:   {"op", "block_id", "kind"},
		core.OperationInsertRelationItem: {"op", "block_id", "relation_id", "item_id", "kind"},
		core.OperationDeleteRelationItem: {"op", "block_id", "relation_id", "item_id"},
		core.OperationMoveRelationItem:   {"op", "block_id", "relation_id", "item_id", "target_block_id", "target_relation_id"},
		core.OperationAttachFile:         {"op", "block_id", "field", "file_id"},
		core.OperationDetachFile:         {"op", "block_id", "field"},
		core.OperationCreateTranslation:  {"op"},
		core.OperationDeleteTranslation:  {"op"},
	} {
		if !reflect.DeepEqual(advertised[string(name)], required) {
			t.Errorf("operation %s requires %v, advertised %v", name, required, advertised[string(name)])
		}
	}
	if len(advertised) != 13 {
		t.Errorf("advertised operations = %v", advertised)
	}
}

// Connectors project tuples to plain arrays and then refuse valid calls, which
// is why inputs were once loose arrays. Named objects avoid that; a tuple must
// not come back.
func TestDocumentSchemasHaveNoTuples(t *testing.T) {
	for name, schema := range map[string]string{
		"mutation input": mutationInputJSONSchema, "read input": readInputJSONSchema, "projection": projectionOutputJSONSchema,
		"text replace": textReplaceInputJSONSchema, "text format": textFormatInputJSONSchema, "file transfer": fileTransferInputJSONSchema,
	} {
		for _, tuple := range []string{`"prefixItems"`, `"items":false`, `"oneOf"`} {
			if strings.Contains(schema, tuple) {
				t.Errorf("%s schema contains %s", name, tuple)
			}
		}
	}
}

func TestOperationsReachApplyThroughHTTPWhileMalformedOnesDoNot(t *testing.T) {
	const documentID = "0c314c79-103b-4e0e-953e-5f9370851639"
	const block = "51e895a2-24c4-4aaa-a28c-fc466c5590fd"
	const parent = "0bb8af09-e358-488b-9950-c07b6d056ca5"
	const after = "8272c725-6186-4c0c-a244-3cb7025ca92e"
	move := func(block, after string) string {
		operation := `{"op":"move_block","block_id":"` + block + `","parent_block_id":"` + parent + `"`
		if after != "" {
			operation += `,"after_block_id":"` + after + `"`
		}
		return operation + `}`
	}
	setField := func(field, value string) string {
		return `{"op":"set_field","block_id":"` + block + `","field":"` + field + `","value":` + value + `}`
	}
	for _, test := range []struct {
		name       string
		operations string
		want       []core.Operation
	}{
		{"valid move batch", `[` + move(block, after) + `,` + move(after, "") + `,` + move(block, after) + `]`, []core.Operation{
			core.MoveBlockOperation(block, parent, after), core.MoveBlockOperation(after, parent, ""), core.MoveBlockOperation(block, parent, after),
		}},
		{"mixed recursive values", `[` + setField("enabled", `{"type":"boolean","boolean":true}`) + `,` + setField("width", `{"type":"number","number":"12.5"}`) + `,` +
			setField("metadata", `{"type":"object","fields":[{"field":"nested","value":{"type":"list","items":[{"value":{"type":"rich_text","content":[{"type":"bold","content":[{"type":"text","text":"text"}]}]}}]}}]}`) + `]`, []core.Operation{
			core.SetFieldOperation(block, "enabled", core.Boolean(true)),
			core.SetFieldOperation(block, "width", core.Number("12.5")),
			core.SetFieldOperation(block, "metadata", core.Object(core.ObjectValue("nested", core.List(core.PositionalItem(core.RichText(core.Bold(core.InlineText("text")))))))),
		}},
		{"missing block", `[{"op":"move_block","parent_block_id":"` + parent + `"}]`, nil},
		{"field of another operation", `[{"op":"move_block","block_id":"` + block + `","kind":"paragraph"}]`, nil},
		{"unknown field", `[{"op":"move_block","block_id":"` + block + `","extra":"x"}]`, nil},
		{"non-string parent", `[{"op":"move_block","block_id":"` + block + `","parent_block_id":false}]`, nil},
		{"unknown operation", `[{"op":"move","block_id":"` + block + `"}]`, nil},
		{"native numeric payload", `[` + setField("width", `{"type":"number","number":12.5}`) + `]`, nil},
		{"wrong value field", `[` + setField("metadata", `{"type":"text","content":[]}`) + `]`, nil},
		{"null inline payload", `[` + setField("content", `{"type":"rich_text","content":[{"type":"text","text":null}]}`) + `]`, nil},
		{"a tuple", `[["fs",["` + block + `","","","content"],["t","x"]]]`, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			application := &recordingAIDocumentApplication{applyResult: core.ApplyResult{DocumentRevision: "revision-b", Normalized: test.want}}
			tools := mustAIDocumentTools(t, application)
			config := validHTTPConfig(nil)
			config.Registry, config.Dispatcher = tools, tools
			response := httptest.NewRecorder()
			request := mcpHTTPRequest(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"document_apply","arguments":{"document_type":"page","document_id":"` + documentID + `","locale":"en","expected_document_revision":"0af8a257-9da1-4a5d-bd7c-44d2b0b63a1f","operations":` + test.operations + `}}}`)
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
					t.Fatal("a malformed operation reached the application")
				}
			} else if application.applyCalls != 1 || !reflect.DeepEqual(application.applyRequest.Operations, test.want) {
				t.Fatalf("decoded batch = %+v", application.applyRequest)
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
		ToolDocumentRead, ToolBlockCreate, ToolBlockUpdate, ToolBlockDelete, ToolDocumentApply,
	} {
		if !strings.Contains(listResponse.Body.String(), `"name":"`+name+`"`) {
			t.Fatalf("tools/list omitted %q: %s", name, listResponse.Body.String())
		}
	}

	callRequest := mcpHTTPRequest(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"document_read","arguments":{"document_type":"page","document_id":"55555555-5555-4555-8555-555555555555","locale":"ko","mode":"metadata"}}}`)
	callResponse := httptest.NewRecorder()
	handler.ServeHTTP(callResponse, callRequest)
	if callResponse.Code != 200 || !strings.Contains(callResponse.Body.String(), `"catalog_fingerprint":"catalog-page"`) {
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

	opened, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentOpen, toolArguments(t, `{"document_type":"post","document_id":"44444444-4444-4444-8444-444444444444","locale":"en"}`))
	if err != nil {
		t.Fatalf("document_open error = %v", err)
	}
	if opened.IsError || stringValue(t, opened.StructuredContent, "document_type") != string(core.DomainPost) || strings.Contains(opened.Content[0]["text"].(string), "profile") {
		t.Fatalf("document_open result is not compact DCDP/1: %+v", opened)
	}
	wantOpen := core.OpenRequest{Document: core.DocumentIdentity{Domain: core.DomainPost, Reference: "44444444-4444-4444-8444-444444444444"}, Locale: "en"}
	if !reflect.DeepEqual(application.openRequest, wantOpen) {
		t.Fatalf("Open request = %+v, want %+v", application.openRequest, wantOpen)
	}

	read, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentRead, toolArguments(t, `{"document_type":"post","document_id":"44444444-4444-4444-8444-444444444444","locale":"en","mode":"fields","fields":[{"block_id":"paragraph-a","field":"content"},{"block_id":"paragraph-a","relation_id":"credits","item_id":"credit-main","field":"name"}],"limit":12,"cursor":"cursor-a"}`))
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
	if !strings.Contains(text, `{"field":"content","value":{"type":"text","text":""}}`) {
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
		toolArguments(t, `{"document_type":"post","document_id":"test-post","locale":"ko"}`),
	)
	var executionErr *mcpserver.ToolExecutionError
	if result.Content != nil || !errors.As(err, &executionErr) || executionErr.Message != "document_id must be a canonical UUID" {
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
		toolArguments(t, `{"document_type":"post","document_id":"44444444-4444-4444-8444-444444444444","locale":"ko"}`),
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
	if validated.IsError || !strings.Contains(validated.Content[0]["text"].(string), `"operations":[{"op":"set_field"`) {
		t.Fatalf("document_validate did not return normalized operations: %+v", validated)
	}

	applied, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentApply, arguments)
	if err != nil {
		t.Fatalf("document_apply error = %v", err)
	}
	if applied.IsError || applied.Content[0]["text"] != `{"changes":[{"affected_handles":["field:paragraph-a/content","translation:en"],"op":"set_field","operation_index":0}],"document_revision":"revision-a","target_revision":"target-revision-b"}` {
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
			want: `"issues":[{"code":"target_field_forbidden","handle":"field:paragraph-a/content","message":"target locale cannot mutate shared field","operation_index":0}]`,
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
		{name: "unknown open field", tool: ToolDocumentOpen, arguments: `{"document_type":"post","document_id":"44444444-4444-4444-8444-444444444444","locale":"en","html":"<p>x</p>"}`},
		{name: "positional field selector", tool: ToolDocumentRead, arguments: `{"document_type":"post","document_id":"44444444-4444-4444-8444-444444444444","locale":"en","mode":"fields","fields":[{"block_id":"x","field":"y","extra":["paragraph-a","content","extra"]}]}`},
		{name: "generic JSON patch", tool: ToolDocumentApply, arguments: `{"document_type":"post","document_id":"44444444-4444-4444-8444-444444444444","locale":"en","expected_document_revision":"revision-a","operations":[{"op":"json_patch"}]}`},
		{name: "raw yjs", tool: ToolDocumentApply, arguments: `{"document_type":"post","document_id":"44444444-4444-4444-8444-444444444444","locale":"en","expected_document_revision":"revision-a","operations":[{"op":"yjs"}]}`},
		{name: "raw html", tool: ToolDocumentApply, arguments: `{"document_type":"post","document_id":"44444444-4444-4444-8444-444444444444","locale":"en","expected_document_revision":"revision-a","operations":[{"op":"set_field","block_id":"paragraph-a","field":"content","value":["html","<p>x</p>"]}]}`},
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
				arguments = toolArguments(t, `{"document_type":"post","document_id":"44444444-4444-4444-8444-444444444444","locale":"en"}`)
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

func mustAIDocumentTools(t *testing.T, application *recordingAIDocumentApplication) blockKindTools {
	t.Helper()
	tools, err := NewAIDocumentTools(application)
	if err != nil {
		t.Fatalf("NewAIDocumentTools() error = %v", err)
	}
	return blockKindTools{tools}
}

// The names these tests give each block kind and read, and the listed tool and
// argument that reach it.
const (
	ToolDocumentOpen     = "document_open"
	ToolParagraphCreate  = "document_paragraph_create"
	ToolParagraphUpdate  = "document_paragraph_update"
	ToolHeadingCreate    = "document_heading_create"
	ToolHeadingUpdate    = "document_heading_update"
	ToolTableCreate      = "document_table_create"
	ToolDocumentValidate = "document_validate"
)

var listedBlockKindCalls = map[string]struct{ tool, argument, value string }{
	ToolDocumentOpen:     {ToolDocumentRead, "mode", `"metadata"`},
	ToolParagraphCreate:  {ToolBlockCreate, "kind", `"paragraph"`},
	ToolParagraphUpdate:  {ToolBlockUpdate, "kind", `"paragraph"`},
	ToolHeadingCreate:    {ToolBlockCreate, "kind", `"heading"`},
	ToolHeadingUpdate:    {ToolBlockUpdate, "kind", `"heading"`},
	ToolTableCreate:      {ToolBlockCreate, "kind", `"table"`},
	ToolDocumentValidate: {ToolDocumentApply, "dry_run", `true`},
}

// blockKindTools calls AIDocumentTools through the tools it lists.
type blockKindTools struct{ *AIDocumentTools }

func (tools blockKindTools) CallTool(ctx context.Context, principal mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	call, renamed := listedBlockKindCalls[name]
	if !renamed {
		return tools.AIDocumentTools.CallTool(ctx, principal, name, arguments)
	}
	arguments = maps.Clone(arguments)
	if arguments == nil {
		arguments = mcpserver.ToolArguments{}
	}
	arguments[call.argument] = json.RawMessage(call.value)
	return tools.AIDocumentTools.CallTool(ctx, principal, call.tool, arguments)
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

func TestPlainTextToolsRefuseFormattedBlocksAndReadDefaultsFitOnePage(t *testing.T) {
	formatted := core.Projection{Protocol: core.ProtocolVersion, Mode: core.ReadFields, Nodes: []core.Node{{
		ID: "paragraph-a", Kind: "paragraph",
		Localized: []core.FieldValue{{ID: "content", Value: core.RichText(core.InlineItem{Kind: core.InlineKindLink, Target: "https://example.com", Children: []core.InlineItem{core.InlineText("video")}})}},
	}}}
	application := &recordingAIDocumentApplication{readResult: formatted}
	_, err := mustAIDocumentTools(t, application).CallTool(t.Context(), mcpserver.Principal{}, ToolParagraphUpdate, toolArguments(t, `{"document_type":"post","document_id":"`+syncTestDocument+`","locale":"ko","expected_document_revision":"current","block_id":"paragraph-a","text":"video"}`))
	var execution *mcpserver.ToolExecutionError
	if !errors.As(err, &execution) || !strings.Contains(execution.Message, "document_apply") || application.applyCalls != 0 {
		t.Fatalf("formatted paragraph replacement = %v, apply calls %d", err, application.applyCalls)
	}

	for mode, want := range map[string]int{"outline": core.DefaultOutlineReadLimit, "blocks": core.DefaultValueReadLimit} {
		reader := &recordingAIDocumentApplication{readResult: core.Projection{}}
		arguments := `{"document_type":"post","document_id":"` + syncTestDocument + `","locale":"ko","mode":"` + mode + `"`
		if mode == "blocks" {
			arguments += `,"block_ids":["paragraph-a"]`
		}
		_, _ = mustAIDocumentTools(t, reader).CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentRead, toolArguments(t, arguments+`}`))
		if reader.readRequest.Limit != want {
			t.Fatalf("%s default limit = %d, want %d", mode, reader.readRequest.Limit, want)
		}
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

func TestDocumentApplyDryRunChecksWithoutApplying(t *testing.T) {
	application := &recordingAIDocumentApplication{validateResult: core.ValidationResult{Normalized: []core.Operation{core.DeleteBlockOperation("paragraph-a")}}}
	result, err := mustAIDocumentTools(t, application).CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentApply, toolArguments(t,
		`{"document_type":"post","document_id":"`+syncTestDocument+`","locale":"ko","expected_document_revision":"r","dry_run":true,"operations":[{"op":"delete_block","block_id":"paragraph-a"}]}`))
	if err != nil || result.IsError || application.applyCalls != 0 || result.StructuredContent["operations"] == nil {
		t.Fatalf("result = %+v, %v, apply calls %d", result, err, application.applyCalls)
	}
}

func TestDocumentReadMetadataReturnsNoBlocks(t *testing.T) {
	application := &recordingAIDocumentApplication{openResult: core.OpenMetadata{
		Protocol: core.ProtocolVersion, Profile: core.DomainPost, Catalog: "catalog", Document: syncTestDocument,
		DocumentRevision: "r", SourceLocale: "ko", Locale: "ko", LocaleRole: core.LocaleRoleSource, LocaleExists: true,
	}}
	result, err := mustAIDocumentTools(t, application).CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentRead, toolArguments(t,
		`{"document_type":"post","document_id":"`+syncTestDocument+`","locale":"ko","mode":"metadata"}`))
	if err != nil || result.StructuredContent["document_revision"] != "r" || result.StructuredContent["blocks"] != nil || application.readCalls != 0 {
		t.Fatalf("result = %+v, %v", result, err)
	}
}

func TestDocumentBlockToolsReachEachKindAndRefuseOthers(t *testing.T) {
	application := &recordingAIDocumentApplication{
		openResult:  core.OpenMetadata{DocumentRevision: "current"},
		readResult:  core.Projection{DocumentRevision: "current"},
		applyResult: core.ApplyResult{DocumentRevision: "next", Changed: true, Changes: []core.Change{{Kind: core.OperationInsertBlock, AffectedHandles: []string{"block:new"}}}},
	}
	tools := mustAIDocumentTools(t, application)
	target := `"document_type":"post","document_id":"` + syncTestDocument + `","locale":"ko"`
	for kind, fields := range map[string]string{"paragraph": `"text":"a"`, "heading": `"text":"a","level":2`, "table": `"rows":[["a"]]`} {
		application.applyCalls = 0
		result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolBlockCreate, toolArguments(t, `{"kind":"`+kind+`",`+target+`,`+fields+`}`))
		if err != nil || result.IsError || application.applyCalls != 1 {
			t.Fatalf("%s: %+v, %v", kind, result, err)
		}
		if got := application.applyRequest.Operations[0].InsertBlock.Kind; string(got) != kind {
			t.Fatalf("%s created %s", kind, got)
		}
	}
	for name, arguments := range map[string]string{
		"unknown kind":          `{"kind":"image",` + target + `}`,
		"field of another kind": `{"kind":"paragraph",` + target + `,"text":"a","rows":[["a"]]}`,
	} {
		if _, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolBlockCreate, toolArguments(t, arguments)); err == nil {
			t.Fatalf("%s succeeded", name)
		}
	}
	if _, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolBlockUpdate, toolArguments(t, `{"kind":"table",`+target+`,"expected_document_revision":"r","block_id":"b","text":"a"}`)); err == nil {
		t.Fatal("a table has no plain-text update")
	}
}
