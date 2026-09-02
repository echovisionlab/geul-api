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

	"connectrpc.com/connect"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/auth"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	testTranslationJobID       = "11111111-1111-4111-8111-111111111111"
	testTranslationGeneratedID = "22222222-2222-4222-8222-222222222222"
	testTranslationArtifactID  = "33333333-3333-4333-8333-333333333333"
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
		ToolTranslationJobsList:    toolAnnotations(true, false, false),
		ToolTranslationList:        toolAnnotations(true, false, false),
		ToolTranslationGet:         toolAnnotations(true, false, false),
		ToolTranslationJobCancel:   toolAnnotations(false, true, false),
		ToolTranslationRegenerate:  toolAnnotations(false, true, true),
		ToolTranslationXLIFFExport: toolAnnotations(false, false, false),
		ToolTranslationXLIFFImport: toolAnnotations(false, true, false),
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
		inputText := strings.ToLower(string(tool.InputSchema))
		for _, forbidden := range []string{"xml", "base64", "blob", "bytes", "content_html", "content_json", "tiptap", "prosemirror", "yjs"} {
			if strings.Contains(inputText, forbidden) {
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

func TestTranslationToolsListJobsMapsBoundedGeneratedRequest(t *testing.T) {
	application := &recordingTranslationApplication{listJobsResponse: &managev1.ListTranslationJobsResponse{
		Jobs:       []*managev1.TranslationJob{testTranslationJob(testTranslationJobID, managev1.TranslationJobStatus_TRANSLATION_JOB_STATUS_RUNNING)},
		Pagination: &commonv1.PaginationResponse{Total: 3, Limit: 2, Offset: 1, HasMore: false},
	}}
	tools := mustTranslationTools(t, application)
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolTranslationJobsList, toolArguments(t, `{
		"p":"post_series","d":"44444444-4444-4444-8444-444444444444","tl":"en","sl":"ko",
		"s":["queued","running"],"n":2,"o":1,"k":"updated_at","z":true
	}`))
	if err != nil {
		t.Fatalf("translation_jobs_list error = %v", err)
	}
	request := application.listJobsRequest
	if request == nil || request.Pagination.GetLimit() != 2 || request.Pagination.GetOffset() != 1 || len(request.Filters) != 5 || len(request.Sorts) != 1 {
		t.Fatalf("ListTranslationJobs request = %+v", request)
	}
	if request.Filters[0].Field != "entity_type" || request.Filters[0].Value != "series" || request.Filters[1].Field != "entity_id" || request.Sorts[0].Order != commonv1.SortOrder_SORT_ORDER_DESC {
		t.Fatalf("ListTranslationJobs lost exact target/sort: %+v", request)
	}
	text := result.Content[0]["text"].(string)
	if !strings.Contains(text, `"g":[3,2,1,false]`) || !strings.Contains(text, `"s":"running"`) {
		t.Fatalf("translation_jobs_list result = %s", text)
	}
}

func TestTranslationToolsListAndGetReturnMetadataWithoutTranslationBody(t *testing.T) {
	title := "secret title"
	html := "<p>secret html</p>"
	entry := &managev1.TranslationEntry{
		Target: &managev1.TranslationTarget{EntityType: managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_POST, EntityId: "post-a"},
		Locale: "en", Title: &title, ContentHtml: &html, ContentJson: []byte(`{"type":"doc"}`),
		UpdatedAt: timestamppb.New(time.Date(2026, 8, 23, 1, 2, 3, 0, time.UTC)),
	}
	application := &recordingTranslationApplication{
		listTranslationsResponse: &managev1.ListEntityTranslationsResponse{SourceLocale: "ko", Entries: []*managev1.TranslationEntry{entry}},
		getTranslationResponse:   &managev1.GetEntityTranslationResponse{Entry: entry},
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
	if !strings.Contains(combined, `"l":"en"`) || application.getTranslationRequest.GetLocale() != "en" {
		t.Fatalf("translation metadata/request mismatch: %s %+v", combined, application.getTranslationRequest)
	}
}

func TestTranslationToolsCancelAndRegenerateUseGeneratedApplication(t *testing.T) {
	application := &recordingTranslationApplication{
		cancelResponse: &managev1.CancelTranslationJobResponse{},
		regenerateResponse: &managev1.RegenerateEntityTranslationsResponse{Jobs: []*managev1.TranslationJob{
			testTranslationJob(testTranslationGeneratedID, managev1.TranslationJobStatus_TRANSLATION_JOB_STATUS_QUEUED),
		}},
	}
	tools := mustTranslationTools(t, application)

	cancelled, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolTranslationJobCancel, toolArguments(t, `{"j":"`+testTranslationJobID+`"}`))
	if err != nil {
		t.Fatalf("%s error = %v", ToolTranslationJobCancel, err)
	}
	if got := cancelled.Content[0]["text"].(string); got != `{"j":"`+testTranslationJobID+`"}` {
		t.Fatalf("%s result = %s", ToolTranslationJobCancel, got)
	}
	if got, ok := cancelled.StructuredContent["j"].(string); !ok || got != testTranslationJobID {
		t.Fatalf("%s structured result = %+v", ToolTranslationJobCancel, cancelled.StructuredContent)
	}

	regenerated, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolTranslationRegenerate, toolArguments(t, `{"p":"post","d":"post-a","l":["en","ja"]}`))
	if err != nil {
		t.Fatalf("%s error = %v", ToolTranslationRegenerate, err)
	}
	if !strings.Contains(regenerated.Content[0]["text"].(string), testTranslationGeneratedID) {
		t.Fatalf("%s result = %+v", ToolTranslationRegenerate, regenerated)
	}
	if application.cancelRequest.JobId != testTranslationJobID {
		t.Fatalf("job cancellation request = %+v", application.cancelRequest)
	}
	if application.regenerateRequest.Target.EntityType != managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_POST || !reflect.DeepEqual(application.regenerateRequest.Locales, []string{"en", "ja"}) {
		t.Fatalf("regenerate request = %+v", application.regenerateRequest)
	}
}

func TestTranslationToolsXLIFFUsesOnlyArtifactReferenceAndUploadedFileID(t *testing.T) {
	fileName := "post-a.en.xlf"
	targetRevision := "target-revision-a"
	application := &recordingTranslationApplication{
		exportResponse: &managev1.ExportEntityTranslationXLIFFResponse{
			Artifact: &commonv1.ExpiringMediaRef{
				FileId: testTranslationArtifactID, Url: "https://cdn.example/xliff/signed", ExpiresAt: timestamppb.New(time.Now().UTC().Add(time.Hour)),
				Extension: "xlf", MimeType: "application/xliff+xml", FileName: &fileName,
			},
			SourceLocale: "ko", TargetLocale: "en", TargetRevision: &targetRevision,
			Mode: managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH,
		},
		importResponse: &managev1.ImportEntityTranslationXLIFFResponse{
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
	if application.importRequest.FileId != testTranslationArtifactID || application.importRequest.GetExpectedTargetRevision() != "target-revision-a" || strings.Contains(imported.Content[0]["text"].(string), "xliff") {
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
		{name: "positional unit handle", tool: ToolTranslationXLIFFExport, arguments: `{"p":"post","d":"post-a","l":"en","m":"patch","u":["blocks/2/content"]}`},
		{name: "implicit all regeneration", tool: ToolTranslationRegenerate, arguments: `{"p":"post","d":"post-a","l":[]}`},
		{name: "non UUID job", tool: ToolTranslationJobCancel, arguments: `{"j":"job-one"}`},
		{name: "uppercase UUID job", tool: ToolTranslationJobCancel, arguments: `{"j":"AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"}`},
		{name: "zero UUID file", tool: ToolTranslationXLIFFImport, arguments: `{"p":"post","d":"post-a","l":"en","m":"replace","f":"00000000-0000-0000-0000-000000000000"}`},
		{name: "duplicate locales", tool: ToolTranslationRegenerate, arguments: `{"p":"post","d":"post-a","l":["en","en"]}`},
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

func TestTranslationToolsRejectInlineArtifactReferencesAndInvalidApplicationOutput(t *testing.T) {
	application := &recordingTranslationApplication{exportResponse: &managev1.ExportEntityTranslationXLIFFResponse{
		Artifact: &commonv1.ExpiringMediaRef{
			FileId: testTranslationArtifactID, Url: "data:application/xliff+xml;base64,AAAA",
			ExpiresAt: timestamppb.New(time.Now().UTC().Add(time.Hour)), Extension: "xlf", MimeType: "application/xliff+xml",
		},
		SourceLocale: "ko", TargetLocale: "en", Mode: managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE,
	}}
	tools := mustTranslationTools(t, application)
	if result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolTranslationXLIFFExport, toolArguments(t, `{"p":"post","d":"post-a","l":"en","m":"replace"}`)); err == nil || result.Content != nil {
		t.Fatalf("inline artifact response was exposed: %+v, %v", result, err)
	}

	application.importResponse = &managev1.ImportEntityTranslationXLIFFResponse{
		TargetRevision: "revision-a", AffectedUnitHandles: []string{"blocks/2/content"},
	}
	if result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolTranslationXLIFFImport, toolArguments(t, `{"p":"post","d":"post-a","l":"en","m":"replace","f":"`+testTranslationArtifactID+`"}`)); err == nil || result.Content != nil {
		t.Fatalf("positional affected unit was exposed: %+v, %v", result, err)
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
			tools := mustTranslationTools(t, &recordingTranslationApplication{regenerateError: test.applicationErr})
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolTranslationRegenerate, toolArguments(t, `{"p":"post","d":"post-a","l":["en"]}`))
			var execution *mcpserver.ToolExecutionError
			if errors.As(err, &execution) != test.wantExecution || result.Content != nil {
				t.Fatalf("CallTool() = %+v, %v, execution=%v", result, err, execution)
			}
		})
	}
}

func TestToolSetComposesDocumentAndTranslationToolsThroughPATHTTP(t *testing.T) {
	documentApplication := &recordingAIDocumentApplication{openResult: core.OpenMetadata{
		Protocol: core.ProtocolVersion, Profile: core.DomainPost, Catalog: "catalog-a", Document: "post-a",
		DocumentRevision: "revision-a", SourceLocale: "ko", Locale: "ko", LocaleRole: core.LocaleRoleSource, LocaleExists: true,
	}}
	translationApplication := &recordingTranslationApplication{regenerateResponse: &managev1.RegenerateEntityTranslationsResponse{
		Jobs: []*managev1.TranslationJob{testTranslationJob(testTranslationJobID, managev1.TranslationJobStatus_TRANSLATION_JOB_STATUS_QUEUED)},
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

	callResponse := serveMCPAdapterRequest(handler, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"translation_regenerate","arguments":{"p":"post","d":"post-a","l":["en"]}}}`)
	if callResponse.Code != 200 || !strings.Contains(callResponse.Body.String(), testTranslationJobID) {
		t.Fatalf("tools/call = %d %s", callResponse.Code, callResponse.Body.String())
	}
	user := auth.GetUser(translationApplication.regenerateContext)
	if user == nil || user.MemberID.String() != testMember || user.IdentityID.String() != testIdentity || user.SessionID != "" {
		t.Fatalf("PAT actor context was not preserved: %+v", user)
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

func testTranslationJob(id string, status managev1.TranslationJobStatus) *managev1.TranslationJob {
	return &managev1.TranslationJob{
		Id:           id,
		Target:       &managev1.TranslationTarget{EntityType: managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_POST, EntityId: "post-a"},
		TargetLocale: "en", SourceLocale: "ko", Status: status,
		OperationId: "55555555-5555-4555-8555-555555555555",
		RequestedAt: timestamppb.New(time.Date(2026, 8, 23, 1, 2, 3, 0, time.UTC)),
	}
}

type recordingTranslationApplication struct {
	managev1connect.UnimplementedTranslationServiceHandler

	listJobsRequest          *managev1.ListTranslationJobsRequest
	cancelRequest            *managev1.CancelTranslationJobRequest
	listTranslationsRequest  *managev1.ListEntityTranslationsRequest
	getTranslationRequest    *managev1.GetEntityTranslationRequest
	regenerateRequest        *managev1.RegenerateEntityTranslationsRequest
	exportRequest            *managev1.ExportEntityTranslationXLIFFRequest
	importRequest            *managev1.ImportEntityTranslationXLIFFRequest
	regenerateContext        context.Context
	listJobsResponse         *managev1.ListTranslationJobsResponse
	cancelResponse           *managev1.CancelTranslationJobResponse
	listTranslationsResponse *managev1.ListEntityTranslationsResponse
	getTranslationResponse   *managev1.GetEntityTranslationResponse
	regenerateResponse       *managev1.RegenerateEntityTranslationsResponse
	exportResponse           *managev1.ExportEntityTranslationXLIFFResponse
	importResponse           *managev1.ImportEntityTranslationXLIFFResponse
	listJobsError            error
	cancelError              error
	listTranslationsError    error
	getTranslationError      error
	regenerateError          error
	exportError              error
	importError              error
}

func (application *recordingTranslationApplication) ListTranslationJobs(_ context.Context, request *connect.Request[managev1.ListTranslationJobsRequest]) (*connect.Response[managev1.ListTranslationJobsResponse], error) {
	application.listJobsRequest = request.Msg
	return connectResponse(application.listJobsResponse), application.listJobsError
}

func (application *recordingTranslationApplication) CancelTranslationJob(_ context.Context, request *connect.Request[managev1.CancelTranslationJobRequest]) (*connect.Response[managev1.CancelTranslationJobResponse], error) {
	application.cancelRequest = request.Msg
	return connectResponse(application.cancelResponse), application.cancelError
}

func (application *recordingTranslationApplication) ListEntityTranslations(_ context.Context, request *connect.Request[managev1.ListEntityTranslationsRequest]) (*connect.Response[managev1.ListEntityTranslationsResponse], error) {
	application.listTranslationsRequest = request.Msg
	return connectResponse(application.listTranslationsResponse), application.listTranslationsError
}

func (application *recordingTranslationApplication) GetEntityTranslation(_ context.Context, request *connect.Request[managev1.GetEntityTranslationRequest]) (*connect.Response[managev1.GetEntityTranslationResponse], error) {
	application.getTranslationRequest = request.Msg
	return connectResponse(application.getTranslationResponse), application.getTranslationError
}

func (application *recordingTranslationApplication) RegenerateEntityTranslations(ctx context.Context, request *connect.Request[managev1.RegenerateEntityTranslationsRequest]) (*connect.Response[managev1.RegenerateEntityTranslationsResponse], error) {
	application.regenerateContext = ctx
	application.regenerateRequest = request.Msg
	return connectResponse(application.regenerateResponse), application.regenerateError
}

func (application *recordingTranslationApplication) ExportEntityTranslationXLIFF(_ context.Context, request *connect.Request[managev1.ExportEntityTranslationXLIFFRequest]) (*connect.Response[managev1.ExportEntityTranslationXLIFFResponse], error) {
	application.exportRequest = request.Msg
	return connectResponse(application.exportResponse), application.exportError
}

func (application *recordingTranslationApplication) ImportEntityTranslationXLIFF(_ context.Context, request *connect.Request[managev1.ImportEntityTranslationXLIFFRequest]) (*connect.Response[managev1.ImportEntityTranslationXLIFFResponse], error) {
	application.importRequest = request.Msg
	return connectResponse(application.importResponse), application.importError
}

func connectResponse[T any](message *T) *connect.Response[T] {
	if message == nil {
		return nil
	}
	return connect.NewResponse(message)
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
