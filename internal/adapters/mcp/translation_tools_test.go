package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediaauth"

	"connectrpc.com/connect"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/auth"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	menudomain "github.com/echovisionlab/geul-api/internal/menu"
	"github.com/echovisionlab/geul-api/internal/translation"
	translationapp "github.com/echovisionlab/geul-api/internal/translation/application"
)

const (
	testTranslationArtifactID = "33333333-3333-4333-8333-333333333333"
)

func TestTranslationToolsExposeTypedArtifactOnlySurface(t *testing.T) {
	tools := mustTranslationTools(t, &recordingTranslationApplication{})
	listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(listed) != len(tools.ToolNames()) {
		t.Fatalf("ListTools() returned %d tools, want %d", len(listed), len(tools.ToolNames()))
	}
	wantAnnotations := map[string]map[string]any{
		ToolTranslationList:        toolAnnotations(true, false, false),
		ToolTranslationGet:         toolAnnotations(true, false, false),
		ToolTranslationXLIFFExport: toolAnnotations(false, false, false),
		ToolTranslationXLIFFImport: toolAnnotations(false, true, false),
		ToolTranslationUnitsGet:    toolAnnotations(true, false, false),
		ToolTranslationUnitsPut:    toolAnnotations(false, true, false),
	}
	for _, tool := range listed {
		assertMCPToolOAuthSecurity(t, tool)
		assertMCPToolAnnotations(t, tool, wantAnnotations[tool.Name])
		var input map[string]any
		var output map[string]any
		if err := json.Unmarshal(tool.InputSchema, &input); err != nil || input["type"] != "object" {
			t.Fatalf("%s input schema = %s (%v)", tool.Name, tool.InputSchema, err)
		}
		if err := json.Unmarshal(tool.OutputSchema, &output); err != nil || output["type"] != "object" {
			t.Fatalf("%s output schema = %s (%v)", tool.Name, tool.OutputSchema, err)
		}
		properties := input["properties"].(map[string]any)
		for _, forbidden := range []string{"xml", "base64", "blob", "bytes", "content_html", "content_json", "tiptap", "prosemirror", "yjs"} {
			if _, exists := properties[forbidden]; exists {
				t.Fatalf("%s input schema exposed forbidden payload %q", tool.Name, forbidden)
			}
		}
	}

	for _, forbiddenTool := range []string{"translation_create", "translation_update", "translation_delete", "translation_set", "translation_job_retry"} {
		for _, name := range tools.ToolNames() {
			if name == forbiddenTool {
				t.Fatalf("MCP duplicated DCDP locale CRUD with %q", forbiddenTool)
			}
		}
	}

	listed[0].InputSchema[0] = '['
	listed[0].SecuritySchemes[0].Scopes[0] = "other"
	listed[0].Meta["securitySchemes"].([]mcpserver.ToolSecurityScheme)[0].Scopes[0] = "other"
	listed[0].Annotations["readOnlyHint"] = false
	again, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	if err != nil || again[0].InputSchema[0] != '{' ||
		!reflect.DeepEqual(again[0].SecuritySchemes[0].Scopes, []string{"mcp", "offline_access"}) ||
		!reflect.DeepEqual(again[0].Meta["securitySchemes"].([]mcpserver.ToolSecurityScheme)[0].Scopes, []string{"mcp", "offline_access"}) ||
		again[0].Annotations["readOnlyHint"] != true {
		t.Fatal("ListTools() returned mutable shared definitions")
	}
	if _, err := NewTranslationTools(nil); err == nil {
		t.Fatal("NewTranslationTools(nil) succeeded")
	}
	var typedNil *recordingTranslationApplication
	if _, err := NewTranslationTools(typedNil); err == nil {
		t.Fatal("NewTranslationTools(typed nil) succeeded")
	}
}

func TestTranslationToolsListAndGetReturnMetadataWithoutTranslationBody(t *testing.T) {
	title := "secret title"
	html := "<p>secret html</p>"
	entry := translationapp.Entry{
		EntityType: "post", EntityID: "post-a",
		Locale: "en", Title: &title, ContentHTML: &html, ContentJSON: []byte(`{"type":"doc"}`),
		UpdatedAt: time.Date(2026, 8, 23, 1, 2, 3, 0, time.UTC),
	}
	application := &recordingTranslationApplication{
		listTranslationsResponse: translationapp.EntityTranslations{SourceLocale: "ko", Entries: []translationapp.Entry{entry}},
		getTranslationResponse:   entry,
	}
	tools := mustTranslationTools(t, application)

	listed, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolTranslationList, toolArguments(t, `{"p":"post","d":"post-a"}`))
	if err != nil {
		t.Fatalf("translation_list error = %v", err)
	}
	got, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolTranslationGet, toolArguments(t, `{"p":"post","d":"post-a","l":"en"}`))
	if err != nil {
		t.Fatalf("translation_get error = %v", err)
	}
	combined := listed.Content[0]["text"].(string) + got.Content[0]["text"].(string)
	for _, forbidden := range []string{"secret title", "secret html", "type", "content", "<p>"} {
		if strings.Contains(strings.ToLower(combined), strings.ToLower(forbidden)) {
			t.Fatalf("translation metadata leaked body %q: %s", forbidden, combined)
		}
	}
	if !strings.Contains(combined, `"l":"en"`) || application.getTranslationRequest.Locale != "en" {
		t.Fatalf("translation metadata/request mismatch: %s %+v", combined, application.getTranslationRequest)
	}
}

func TestTranslationToolsXLIFFUsesOnlyArtifactReferenceAndUploadedFileID(t *testing.T) {
	fileName := "post-a.en.xlf"
	targetRevision := "target-revision-a"
	application := &recordingTranslationApplication{
		exportResponse: translationapp.XLIFFExport{
			Artifact: &mediaauth.Reference{
				FileID: testTranslationArtifactID, URL: "https://cdn.example/xliff/signed", ExpiresAt: time.Now().UTC().Add(time.Hour),
				Extension: "xlf", MimeType: "application/xliff+xml", FileName: &fileName,
			},
			SourceLocale: "ko", TargetLocale: "en", TargetRevision: &targetRevision,
			Mode: translationapp.InterchangeModePatch,
		},
		importResponse: translationapp.XLIFFImport{
			TargetRevision: "target-revision-b", Changed: true, AffectedUnitHandles: []string{"block-a/content"},
		},
	}
	tools := mustTranslationTools(t, application)

	exported, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolTranslationXLIFFExport, toolArguments(t, `{
		"p":"post","d":"post-a","l":"en","m":"patch","u":["block-a/content"]
	}`))
	if err != nil {
		t.Fatalf("translation_xliff_export error = %v", err)
	}
	exportText := exported.Content[0]["text"].(string)
	if !strings.Contains(exportText, `"f":"`+testTranslationArtifactID+`"`) || !strings.Contains(exportText, `"u":"https://cdn.example/xliff/signed"`) {
		t.Fatalf("XLIFF export omitted artifact reference: %s", exportText)
	}
	for _, forbidden := range []string{"<xliff", "base64", "blob", "source text", "target text"} {
		if strings.Contains(strings.ToLower(exportText), forbidden) {
			t.Fatalf("XLIFF export leaked payload %q: %s", forbidden, exportText)
		}
	}

	imported, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolTranslationXLIFFImport, toolArguments(t, `{
		"p":"post","d":"post-a","l":"en","m":"patch","f":"`+testTranslationArtifactID+`","er":"target-revision-a"
	}`))
	if err != nil {
		t.Fatalf("translation_xliff_import error = %v", err)
	}
	if application.importRequest.FileID != testTranslationArtifactID || application.importRequest.ExpectedTargetRevision == nil || *application.importRequest.ExpectedTargetRevision != "target-revision-a" || strings.Contains(imported.Content[0]["text"].(string), "xliff") {
		t.Fatalf("XLIFF import request/result = %+v %+v", application.importRequest, imported)
	}
}

func TestTranslationToolsRejectInlineXLIFFAndInvalidLifecycleArguments(t *testing.T) {
	tools := mustTranslationTools(t, &recordingTranslationApplication{})
	tests := []struct {
		name      string
		tool      string
		arguments string
	}{
		{name: "inline XML", tool: ToolTranslationXLIFFImport, arguments: `{"p":"post","d":"post-a","l":"en","m":"patch","f":"` + testTranslationArtifactID + `","xml":"<xliff/>"}`},
		{name: "base64", tool: ToolTranslationXLIFFImport, arguments: `{"p":"post","d":"post-a","l":"en","m":"patch","f":"` + testTranslationArtifactID + `","base64":"AAAA"}`},
		{name: "blob", tool: ToolTranslationXLIFFImport, arguments: `{"p":"post","d":"post-a","l":"en","m":"patch","f":"` + testTranslationArtifactID + `","blob":[1,2]}`},
		{name: "file payload in file ID", tool: ToolTranslationXLIFFImport, arguments: `{"p":"post","d":"post-a","l":"en","m":"patch","f":"data:application/xml;base64,AAAA"}`},
		{name: "patch export without units", tool: ToolTranslationXLIFFExport, arguments: `{"p":"post","d":"post-a","l":"en","m":"patch"}`},
		{name: "replace export with units", tool: ToolTranslationXLIFFExport, arguments: `{"p":"post","d":"post-a","l":"en","m":"replace","u":["block-a/content"]}`},
		{name: "empty unit handle", tool: ToolTranslationXLIFFExport, arguments: `{"p":"post","d":"post-a","l":"en","m":"patch","u":[""]}`},
		{name: "zero UUID file", tool: ToolTranslationXLIFFImport, arguments: `{"p":"post","d":"post-a","l":"en","m":"replace","f":"00000000-0000-0000-0000-000000000000"}`},
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
}

func TestTranslationToolsRejectOptionalNullBeforeApplicationCall(t *testing.T) {
	for _, test := range []struct {
		name      string
		tool      string
		arguments string
	}{
		{"replace export selection", ToolTranslationXLIFFExport, `{"p":"post","d":"post-a","l":"en","m":"replace","u":null}`},
		{"import revision", ToolTranslationXLIFFImport, `{"p":"post","d":"post-a","l":"en","m":"patch","f":"` + testTranslationArtifactID + `","er":null}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			application := &recordingTranslationApplication{}
			tools := mustTranslationTools(t, application)
			_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, test.tool, toolArguments(t, test.arguments))
			var execution *mcpserver.ToolExecutionError
			if !errors.As(err, &execution) || application.exportRequest != nil || application.importRequest != nil {
				t.Fatalf("null XLIFF argument: error=%v, export=%+v, import=%+v", err, application.exportRequest, application.importRequest)
			}
		})
	}
}

func TestTranslationToolsRejectInlineArtifactReferencesAndInvalidApplicationOutput(t *testing.T) {
	application := &recordingTranslationApplication{exportResponse: translationapp.XLIFFExport{
		Artifact: &mediaauth.Reference{
			FileID: testTranslationArtifactID, URL: "data:application/xliff+xml;base64,AAAA",
			ExpiresAt: time.Now().UTC().Add(time.Hour), Extension: "xlf", MimeType: "application/xliff+xml",
		},
		SourceLocale: "ko", TargetLocale: "en", Mode: translationapp.InterchangeModeReplace,
	}}
	tools := mustTranslationTools(t, application)
	if result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolTranslationXLIFFExport, toolArguments(t, `{"p":"post","d":"post-a","l":"en","m":"replace"}`)); err == nil || result.Content != nil {
		t.Fatalf("inline artifact response was exposed: %+v, %v", result, err)
	}

	application.importResponse = translationapp.XLIFFImport{
		TargetRevision: "revision-a", AffectedUnitHandles: []string{""},
	}
	if result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolTranslationXLIFFImport, toolArguments(t, `{"p":"post","d":"post-a","l":"en","m":"replace","f":"`+testTranslationArtifactID+`"}`)); err == nil || result.Content != nil {
		t.Fatalf("empty affected unit was exposed: %+v, %v", result, err)
	}
}

func TestTranslationToolsPreserveOwningMenuUnitHandles(t *testing.T) {
	const menuID = "44444444-4444-4444-8444-444444444444"
	plan, err := menudomain.BuildTranslationExtractionPlan(menuID, "ko", "en", &translation.SourceDocument{
		ContentJSON: []byte(`[{"id":"123","label":"Menu label","linkType":"custom","url":"/"}]`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := translation.BuildXLIFFDocument(plan); err != nil {
		t.Fatal(err)
	}
	handle := plan.Units[0].UnitID
	application := &recordingTranslationApplication{
		exportResponse: translationapp.XLIFFExport{
			Artifact: &mediaauth.Reference{
				FileID: testTranslationArtifactID, URL: "https://cdn.example/xliff/signed",
				ExpiresAt: time.Now().UTC().Add(time.Hour), Extension: "xlf", MimeType: "application/xliff+xml",
			},
			SourceLocale: "ko", TargetLocale: "en", Mode: translationapp.InterchangeModePatch,
		},
		importResponse: translationapp.XLIFFImport{
			TargetRevision: "tr1_after_import", Changed: true, AffectedUnitHandles: []string{handle},
		},
	}
	tools := mustTranslationTools(t, application)
	_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolTranslationXLIFFExport,
		toolArguments(t, `{"p":"menu","d":"`+menuID+`","l":"en","m":"patch","u":["`+handle+`"]}`))
	if err != nil || !reflect.DeepEqual(application.exportRequest.UnitHandles, []string{handle}) {
		t.Fatalf("owning Menu handle export = %+v, %v", application.exportRequest, err)
	}
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolTranslationXLIFFImport,
		toolArguments(t, `{"p":"menu","d":"`+menuID+`","l":"en","m":"replace","f":"`+testTranslationArtifactID+`"}`))
	if err != nil {
		t.Fatalf("successful import was rejected while encoding its owning handle: %v", err)
	}
	if result.StructuredContent["c"] != true || !reflect.DeepEqual(result.StructuredContent["u"], []any{handle}) {
		t.Fatalf("successful import result = %+v", result.StructuredContent)
	}

	// An opaque handle is still subject to the application's current manifest.
	application.exportError = connect.NewError(connect.CodeInvalidArgument, errors.New("unknown unit handles"))
	_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolTranslationXLIFFExport,
		toolArguments(t, `{"p":"menu","d":"`+menuID+`","l":"en","m":"patch","u":["unknown/2"]}`))
	var execution *mcpserver.ToolExecutionError
	if !errors.As(err, &execution) {
		t.Fatalf("owning manifest rejection was not preserved: %v", err)
	}
}

func TestTranslationToolsPreserveSafeConnectErrorsAndHideInternalFailures(t *testing.T) {
	for _, test := range []struct {
		name           string
		applicationErr error
		wantExecution  bool
	}{
		{name: "invalid argument", applicationErr: connect.NewError(connect.CodeInvalidArgument, errors.New("target locale is invalid")), wantExecution: true},
		{name: "permission denied", applicationErr: connect.NewError(connect.CodePermissionDenied, errors.New("no permission")), wantExecution: true},
		{name: "internal", applicationErr: connect.NewError(connect.CodeInternal, errors.New("database password leaked"))},
	} {
		t.Run(test.name, func(t *testing.T) {
			tools := mustTranslationTools(t, &recordingTranslationApplication{getTranslationError: test.applicationErr})
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolTranslationGet, toolArguments(t, `{"p":"post","d":"post-a","l":"en"}`))
			var execution *mcpserver.ToolExecutionError
			if errors.As(err, &execution) != test.wantExecution || result.Content != nil {
				t.Fatalf("CallTool() = %+v, %v, execution=%v", result, err, execution)
			}
		})
	}
}

func TestToolSetComposesDocumentAndTranslationToolsThroughGatewayHTTP(t *testing.T) {
	documentApplication := &recordingAIDocumentApplication{openResult: core.OpenMetadata{
		Protocol: core.ProtocolVersion, Profile: core.DomainPost, Catalog: "catalog-a", Document: "post-a",
		DocumentRevision: "revision-a", SourceLocale: "ko", Locale: "ko", LocaleRole: core.LocaleRoleSource, LocaleExists: true,
	}}
	translationApplication := &recordingTranslationApplication{getTranslationResponse: translationapp.Entry{
		EntityType: "post", EntityID: "post-a", Locale: "en", UpdatedAt: time.Date(2026, 8, 23, 1, 2, 3, 0, time.UTC),
	}}
	documentTools, err := NewAIDocumentTools(documentApplication)
	if err != nil {
		t.Fatal(err)
	}
	translations := mustTranslationTools(t, translationApplication)
	set, err := NewToolSet(documentTools, translations)
	if err != nil {
		t.Fatalf("NewToolSet() error = %v", err)
	}
	config := validHTTPConfig(nil)
	config.Registry = set
	config.Dispatcher = set
	handler := newHTTPTestHandler(t, config)

	listResponse := serveMCPAdapterRequest(handler, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	if listResponse.Code != 200 {
		t.Fatalf("tools/list = %d %s", listResponse.Code, listResponse.Body.String())
	}
	for _, name := range append(documentTools.ToolNames(), translations.ToolNames()...) {
		if !strings.Contains(listResponse.Body.String(), `"name":"`+name+`"`) {
			t.Fatalf("tools/list omitted %q", name)
		}
	}

	callResponse := serveMCPAdapterRequest(handler, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"translation_get","arguments":{"p":"post","d":"post-a","l":"en"}}}`)
	if callResponse.Code != 200 || !strings.Contains(callResponse.Body.String(), `\"l\":\"en\"`) {
		t.Fatalf("tools/call = %d %s", callResponse.Code, callResponse.Body.String())
	}
	user := auth.GetUser(translationApplication.getTranslationContext)
	if user == nil || user.MemberID.String() != testMember || user.IdentityID.String() != testIdentity || user.SessionID != "" {
		t.Fatalf("OAuth actor context was not preserved: %+v", user)
	}
}

func TestToolSetRejectsDuplicateAndTypedNilProviders(t *testing.T) {
	documentTools, err := NewAIDocumentTools(&recordingAIDocumentApplication{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewToolSet(documentTools, documentTools); err == nil {
		t.Fatal("NewToolSet() accepted duplicate tool ownership")
	}
	var typedNil *AIDocumentTools
	if _, err := NewToolSet(typedNil); err == nil {
		t.Fatal("NewToolSet() accepted a typed nil provider")
	}
}

// recordingTranslationApplication records each use-case input and answers
// with the configured result.
type recordingTranslationApplication struct {
	listTranslationsRequest  *translationapp.ListEntityTranslationsQuery
	getTranslationRequest    *translationapp.GetEntityTranslationQuery
	getTranslationContext    context.Context
	exportRequest            *translationapp.ExportTranslationXLIFFCommand
	importRequest            *translationapp.ImportTranslationXLIFFCommand
	listTranslationsResponse translationapp.EntityTranslations
	getTranslationResponse   translationapp.Entry
	exportResponse           translationapp.XLIFFExport
	importResponse           translationapp.XLIFFImport
	listTranslationsError    error
	getTranslationError      error
	exportError              error
	importError              error
	unitsGetRequest          *translationapp.GetTranslationUnitsQuery
	unitsGetResponse         translationapp.TranslationUnits
	unitsPutRequest          *translationapp.PutTranslationUnitsCommand
	unitsPutResponse         translationapp.XLIFFImport
}

func (application *recordingTranslationApplication) ListEntityTranslations(_ context.Context, query translationapp.ListEntityTranslationsQuery) (translationapp.EntityTranslations, error) {
	application.listTranslationsRequest = &query
	return application.listTranslationsResponse, application.listTranslationsError
}

func (application *recordingTranslationApplication) GetEntityTranslation(ctx context.Context, query translationapp.GetEntityTranslationQuery) (translationapp.Entry, error) {
	application.getTranslationContext = ctx
	application.getTranslationRequest = &query
	return application.getTranslationResponse, application.getTranslationError
}

func (application *recordingTranslationApplication) ExportEntityTranslationXLIFF(_ context.Context, command translationapp.ExportTranslationXLIFFCommand) (translationapp.XLIFFExport, error) {
	application.exportRequest = &command
	return application.exportResponse, application.exportError
}

func (application *recordingTranslationApplication) ImportEntityTranslationXLIFF(_ context.Context, command translationapp.ImportTranslationXLIFFCommand) (translationapp.XLIFFImport, error) {
	application.importRequest = &command
	return application.importResponse, application.importError
}

func (application *recordingTranslationApplication) GetEntityTranslationUnits(_ context.Context, query translationapp.GetTranslationUnitsQuery) (translationapp.TranslationUnits, error) {
	application.unitsGetRequest = &query
	return application.unitsGetResponse, nil
}

func (application *recordingTranslationApplication) PutEntityTranslationUnits(_ context.Context, command translationapp.PutTranslationUnitsCommand) (translationapp.XLIFFImport, error) {
	application.unitsPutRequest = &command
	return application.unitsPutResponse, nil
}

func mustTranslationTools(t *testing.T, application *recordingTranslationApplication) *TranslationTools {
	t.Helper()
	tools, err := NewTranslationTools(application)
	if err != nil {
		t.Fatalf("NewTranslationTools() error = %v", err)
	}
	return tools
}

func serveMCPAdapterRequest(handler interface {
	ServeHTTP(http.ResponseWriter, *http.Request)
}, body string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, mcpHTTPRequest(body))
	return response
}

func TestTranslationToolsRejectXLIFFArtifactWithoutExpiry(t *testing.T) {
	application := &recordingTranslationApplication{exportResponse: translationapp.XLIFFExport{
		Artifact:     &mediaauth.Reference{FileID: testTranslationArtifactID, URL: "https://cdn.example/xliff/signed", Extension: "xlf", MimeType: "application/xliff+xml"},
		SourceLocale: "ko", TargetLocale: "en", Mode: translationapp.InterchangeModeReplace,
	}}
	tools := mustTranslationTools(t, application)
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolTranslationXLIFFExport, toolArguments(t, `{"p":"post","d":"post-a","l":"en","m":"replace"}`))
	if err == nil || !strings.Contains(err.Error(), "incomplete XLIFF artifact reference") || result.Content != nil {
		t.Fatalf("missing artifact expiry: result=%+v, error=%v", result, err)
	}
}

func TestTranslationUnitsToolsPassUnitsAndMarkupThrough(t *testing.T) {
	revision := "revision-a"
	application := &recordingTranslationApplication{
		unitsGetResponse: translationapp.TranslationUnits{
			SourceLocale: "en", TargetLocale: "ko", TargetRevision: &revision,
			Units: []translation.XLIFFUnitMarkup{{ID: "entity:title", GroupID: "entity:meta", Source: `<pc id="r1">Night</pc> walks`}},
		},
		unitsPutResponse: translationapp.XLIFFImport{TargetRevision: "revision-b", Changed: true, AffectedUnitHandles: []string{"entity:title"}},
	}
	tools := mustTranslationTools(t, application)

	read, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolTranslationUnitsGet, toolArguments(t, `{"p":"post","d":"post-a","l":"ko","u":["entity:title"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := application.unitsGetRequest; got == nil || got.TargetLocale != "ko" || !reflect.DeepEqual(got.UnitHandles, []string{"entity:title"}) {
		t.Fatalf("units get request = %+v", got)
	}
	units := read.StructuredContent["u"].([]any)
	if read.StructuredContent["r"] != revision || len(units) != 1 ||
		units[0].(map[string]any)["s"] != `<pc id="r1">Night</pc> walks` {
		t.Fatalf("units get result = %+v", read.StructuredContent)
	}

	written, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolTranslationUnitsPut, toolArguments(t, `{"p":"post","d":"post-a","l":"ko","er":"revision-a","u":{"entity:title":"<pc id=\"r1\">밤</pc> 산책"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := application.unitsPutRequest; got == nil || *got.ExpectedTargetRevision != revision ||
		got.Targets["entity:title"] != `<pc id="r1">밤</pc> 산책` {
		t.Fatalf("units put request = %+v", got)
	}
	if written.StructuredContent["r"] != "revision-b" || written.StructuredContent["c"] != true {
		t.Fatalf("units put result = %+v", written.StructuredContent)
	}
}

func TestTranslationUnitsPutRejectsAnEmptyTargetSet(t *testing.T) {
	application := &recordingTranslationApplication{}
	tools := mustTranslationTools(t, application)

	_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolTranslationUnitsPut, toolArguments(t, `{"p":"post","d":"post-a","l":"ko","u":{}}`))

	if err == nil || application.unitsPutRequest != nil {
		t.Fatalf("empty target set: error=%v request=%+v", err, application.unitsPutRequest)
	}
}
