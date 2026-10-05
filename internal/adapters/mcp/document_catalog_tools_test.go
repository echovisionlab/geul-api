package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect"
	core "github.com/echovisionlab/geul-api/internal/aidocument"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
)

type documentCatalogPort struct {
	document core.Document
	loadErr  error
	request  core.OpenRequest
	loads    int
}

func (p *documentCatalogPort) Load(_ context.Context, identity core.DocumentIdentity, locale core.Locale) (core.Document, error) {
	p.loads++
	p.request = core.OpenRequest{Document: identity, Locale: locale}
	return p.document, p.loadErr
}

func (*documentCatalogPort) ValidateMutation(context.Context, core.ApplyRequest) (core.ValidationResult, error) {
	return core.ValidationResult{}, errors.New("catalog discovery cannot validate mutations")
}

func (*documentCatalogPort) ExecuteMutation(context.Context, core.ApplyRequest) (core.ApplyResult, error) {
	return core.ApplyResult{}, errors.New("catalog discovery cannot execute mutations")
}

func documentCatalogFixture() core.Document {
	text := core.FieldSchema{Kind: core.ValueKindText, Ownership: core.FieldOwnershipSource}
	file := core.FieldSchema{Ownership: core.FieldOwnershipShared, File: true}
	object := core.FieldSchema{Kind: core.ValueKindObject, Ownership: core.FieldOwnershipSource, Fields: []core.NestedFieldRule{
		{Field: "name", Schema: text}, {Field: "asset", Schema: file},
	}}
	list := core.FieldSchema{Kind: core.ValueKindList, Ownership: core.FieldOwnershipSource, Item: &object, Identity: core.ListIdentityRule{Kind: core.ListIdentityField, Field: "name"}}
	fixed := core.FieldSchema{Kind: core.ValueKindList, Ownership: core.FieldOwnershipSource, Item: &text, Identity: core.ListIdentityRule{Kind: core.ListIdentityFixed, Handles: []core.RelationItemID{"vertex", "common"}}}
	value := core.FieldSchema{Kind: core.ValueKindList, Ownership: core.FieldOwnershipSource, Item: &text, Identity: core.ListIdentityRule{Kind: core.ListIdentityValue}}
	positional := core.FieldSchema{Kind: core.ValueKindList, Ownership: core.FieldOwnershipSource, Item: &text, Identity: core.ListIdentityRule{Kind: core.ListIdentityPositional}}
	target := core.Revision("target-rev")
	return core.Document{
		Identity:     core.DocumentIdentity{Domain: core.DomainPage, Reference: "44444444-4444-4444-8444-444444444444"},
		SourceLocale: "en", Locale: "ko", LocaleExists: true, DocumentRevision: "shared-rev", TargetRevision: &target,
		Catalog: core.Catalog{
			Fingerprint: "typed-catalog", BlockKinds: []core.BlockKind{"shader", "document"},
			Fields: []core.FieldRule{
				{BlockKind: "shader", Field: "units", ValueKind: list.Kind, Ownership: list.Ownership, Schema: &list},
				{BlockKind: "shader", Field: "fixed", ValueKind: fixed.Kind, Ownership: fixed.Ownership, Schema: &fixed},
				{BlockKind: "shader", Field: "values", ValueKind: value.Kind, Ownership: value.Ownership, Schema: &value},
				{BlockKind: "shader", Field: "positional", ValueKind: positional.Kind, Ownership: positional.Ownership, Schema: &positional},
				{BlockKind: "document", Field: "title", ValueKind: core.ValueKindText, Ownership: core.FieldOwnershipLocale, Translatable: true},
				{BlockKind: "document", Field: "enabled", ValueKind: core.ValueKindBoolean, Ownership: core.FieldOwnershipShared},
				{BlockKind: "document", Field: "amount", ValueKind: core.ValueKindNumber, Ownership: core.FieldOwnershipSource},
				{BlockKind: "document", Field: "content", ValueKind: core.ValueKindInline, Ownership: core.FieldOwnershipLocale, Translatable: true},
				{BlockKind: "document", Field: "hero", Ownership: core.FieldOwnershipSource, File: true},
			},
			Relations: []core.RelationRule{{BlockKind: "document", Relation: "credits", ItemKinds: []core.RelationItemKind{"member", "artist"}}},
			RelationFields: []core.RelationFieldRule{
				{BlockKind: "document", Relation: "credits", ItemKind: "artist", Field: "details", ValueKind: core.ValueKindObject, Ownership: core.FieldOwnershipSource, Schema: &object},
				{BlockKind: "document", Relation: "credits", ItemKind: "member", Field: "avatar", Ownership: core.FieldOwnershipShared, File: true},
			},
		},
		Nodes: []core.Node{{ID: "private-block", Kind: "document", Localized: []core.FieldValue{{ID: "title", Value: core.Text("private document value")}}}},
	}
}

func mustDocumentCatalogTools(t *testing.T, port *documentCatalogPort) *DocumentCatalogTools {
	t.Helper()
	service, err := core.NewService(port)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := NewDocumentCatalogTools(service)
	if err != nil {
		t.Fatal(err)
	}
	return tools
}

func TestDocumentCatalogReturnsAuthorizedRecursiveTypedShapes(t *testing.T) {
	port := &documentCatalogPort{document: documentCatalogFixture()}
	tools := mustDocumentCatalogTools(t, port)
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentCatalog, toolArguments(t, `{"p":"page","d":"44444444-4444-4444-8444-444444444444","l":"ko"}`))
	if err != nil {
		t.Fatal(err)
	}
	if port.loads != 1 || port.request.Document != port.document.Identity || port.request.Locale != "ko" || result.IsError {
		t.Fatalf("owning read request/result = %+v / %+v", port.request, result)
	}
	output := result.StructuredContent
	for key, want := range map[string]any{"v": core.ProtocolVersion, "p": "page", "d": string(port.document.Identity.Reference), "c": "typed-catalog", "dr": "shared-rev", "tr": "target-rev", "s": "en", "l": "ko", "lr": "non_source", "le": true} {
		if output[key] != want {
			t.Fatalf("metadata %s=%v, want %v", key, output[key], want)
		}
	}
	if !reflect.DeepEqual(output["block_kinds"], []any{"document", "shader"}) {
		t.Fatalf("block kinds=%v", output["block_kinds"])
	}
	fields := map[string]map[string]any{}
	for _, raw := range output["fields"].([]any) {
		field := raw.(map[string]any)
		fields[field["field"].(string)] = field
	}
	if fields["title"]["ownership"] != "locale" || fields["title"]["translatable"] != true || fields["title"]["kind"] != "t" {
		t.Fatalf("title=%v", fields["title"])
	}
	for field, kind := range map[string]string{"enabled": "b", "amount": "n", "content": "i"} {
		if fields[field]["kind"] != kind {
			t.Fatalf("%s kind=%v, want %s", field, fields[field]["kind"], kind)
		}
	}
	if fields["hero"]["ownership"] != "source" || fields["hero"]["file"] != true || fields["hero"]["kind"] != nil {
		t.Fatalf("File shape=%v", fields["hero"])
	}
	units := fields["units"]
	identity := units["identity"].(map[string]any)
	if units["kind"] != "l" || identity["kind"] != "field" || identity["field"] != "name" {
		t.Fatalf("stable list=%v", units)
	}
	item := units["item"].(map[string]any)
	nested := item["fields"].([]any)
	asset := nested[0].(map[string]any)
	assetShape := asset["schema"].(map[string]any)
	if item["kind"] != "o" || asset["field"] != "asset" || assetShape["file"] != true || assetShape["ownership"] != "shared" || assetShape["kind"] != nil {
		t.Fatalf("nested File=%v", units)
	}
	if got := fields["fixed"]["identity"].(map[string]any)["handles"]; !reflect.DeepEqual(got, []any{"vertex", "common"}) {
		t.Fatalf("fixed handles=%v", got)
	}
	for _, kind := range []string{"value", "positional"} {
		name := kind
		if kind == "value" {
			name = "values"
		}
		if fields[name]["identity"].(map[string]any)["kind"] != kind {
			t.Fatalf("%s identity=%v", name, fields[name])
		}
	}
	relation := output["relations"].([]any)[0].(map[string]any)
	if relation["relation"] != "credits" || !reflect.DeepEqual(relation["item_kinds"], []any{"artist", "member"}) {
		t.Fatalf("relations=%v", relation)
	}
	relationFields := output["relation_fields"].([]any)
	if len(relationFields) != 2 || relationFields[0].(map[string]any)["kind"] != "o" || relationFields[1].(map[string]any)["file"] != true {
		t.Fatalf("relation fields=%v", relationFields)
	}
	encoded := result.Content[0]["text"].(string)
	if strings.Contains(encoded, "private document value") || strings.Contains(encoded, "private-block") {
		t.Fatalf("catalog leaked native/document values: %s", encoded)
	}
	var textOutput map[string]any
	if err := json.Unmarshal([]byte(encoded), &textOutput); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(textOutput, output) {
		t.Fatal("content/structured catalog outputs differ")
	}
}

func TestDocumentCatalogOwningDenialAndInputErrorsReturnNoCatalog(t *testing.T) {
	for _, code := range []connect.Code{connect.CodePermissionDenied, connect.CodeNotFound} {
		t.Run(code.String(), func(t *testing.T) {
			port := &documentCatalogPort{document: documentCatalogFixture(), loadErr: connect.NewError(code, errors.New("owning read denied"))}
			result, err := mustDocumentCatalogTools(t, port).CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentCatalog, toolArguments(t, `{"p":"page","d":"44444444-4444-4444-8444-444444444444","l":"ko"}`))
			var execution *mcpserver.ToolExecutionError
			if !errors.As(err, &execution) || port.loads != 1 || result.StructuredContent != nil || result.Content != nil {
				t.Fatalf("denial exposed catalog: result=%+v error=%v", result, err)
			}
		})
	}
	for _, input := range []string{
		`{"p":"page","d":"slug","l":"ko"}`,
		`{"p":"page","d":null,"l":"ko"}`,
		`{"p":"page","d":"44444444-4444-4444-8444-444444444444","l":"ko","catalog":"override"}`,
		`{"p":"unknown","d":"44444444-4444-4444-8444-444444444444","l":"ko"}`,
		`{"p":"page","d":"44444444-4444-4444-8444-444444444444","l":null}`,
	} {
		port := &documentCatalogPort{document: documentCatalogFixture()}
		result, err := mustDocumentCatalogTools(t, port).CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentCatalog, toolArguments(t, input))
		if err == nil || port.loads != 0 || result.StructuredContent != nil {
			t.Fatalf("invalid arguments reached owning load: %s result=%+v err=%v", input, result, err)
		}
	}
}

func TestDocumentCatalogToolSchemasAndRegistry(t *testing.T) {
	if _, err := NewDocumentCatalogTools(nil); err == nil {
		t.Fatal("nil application accepted")
	}
	var typedNil *core.Service
	if _, err := NewDocumentCatalogTools(typedNil); err == nil {
		t.Fatal("typed nil application accepted")
	}
	tools := mustDocumentCatalogTools(t, &documentCatalogPort{document: documentCatalogFixture()})
	if !reflect.DeepEqual(tools.ToolNames(), []string{ToolDocumentCatalog}) {
		t.Fatal(tools.ToolNames())
	}
	listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	if err != nil || len(listed) != 1 {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	assertMCPToolOAuthSecurity(t, listed[0])
	assertMCPToolAnnotations(t, listed[0], toolAnnotations(true, false, false))
	if !strings.Contains(listed[0].Description, "compiler enforces domain constraints") {
		t.Fatal(listed[0].Description)
	}
	var schema map[string]any
	if err := json.Unmarshal(listed[0].OutputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	assertSchemaRootRefsResolve(t, schema, schema)
	properties := schema["properties"].(map[string]any)
	for _, name := range []string{"block_kinds", "fields", "relations", "relation_fields"} {
		if properties[name] == nil {
			t.Fatalf("missing output property %s", name)
		}
	}
	shape := schema["$defs"].(map[string]any)["shape"].(map[string]any)["properties"].(map[string]any)
	for _, name := range []string{"kind", "ownership", "translatable", "file", "item", "fields", "identity"} {
		if shape[name] == nil {
			t.Fatalf("missing recursive property %s", name)
		}
	}
	for _, invented := range []string{"default", "minimum", "maximum", "enum_values", "required_value"} {
		if shape[invented] != nil {
			t.Fatalf("invented domain constraint %s", invented)
		}
	}
	listed[0].OutputSchema[0] = '['
	again, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	if err != nil || again[0].OutputSchema[0] != '{' {
		t.Fatal("tool schemas share mutable registry bytes")
	}
	if _, err := tools.CallTool(t.Context(), mcpserver.Principal{}, "unknown", nil); !errors.Is(err, mcpserver.ErrUnknownTool) {
		t.Fatalf("unknown tool=%v", err)
	}
}
