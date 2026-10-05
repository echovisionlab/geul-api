package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"connectrpc.com/connect"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestProgramEventReferenceDescriptors(t *testing.T) {
	tools := newProgramEventReferenceFixture(t, &recordingProgramEventReferences{})
	listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	if err != nil || len(listed) != 3 {
		t.Fatalf("tools=%v err=%v", listed, err)
	}
	if !reflect.DeepEqual(tools.ToolNames(), []string{ToolProgramEventTypeList, ToolProgramEventSeriesList, ToolLabelList}) {
		t.Fatal(tools.ToolNames())
	}
	for _, tool := range listed {
		assertMCPToolOAuthSecurity(t, tool)
		if tool.Annotations["readOnlyHint"] != true || tool.Annotations["destructiveHint"] != false || tool.Annotations["openWorldHint"] != false {
			t.Fatalf("%s annotations=%v", tool.Name, tool.Annotations)
		}
		for _, raw := range []json.RawMessage{tool.InputSchema, tool.OutputSchema} {
			var schema map[string]any
			if err := json.Unmarshal(raw, &schema); err != nil || schema["type"] != "object" || schema["additionalProperties"] != false {
				t.Fatalf("%s schema=%s err=%v", tool.Name, raw, err)
			}
		}
		var input map[string]any
		if err := json.Unmarshal(tool.InputSchema, &input); err != nil {
			t.Fatal(err)
		}
		properties := input["properties"].(map[string]any)
		_, hasQuery := properties["query"]
		if hasQuery != (tool.Name != ToolProgramEventTypeList) {
			t.Fatalf("%s query advertised=%v", tool.Name, hasQuery)
		}
		limit := properties["limit"].(map[string]any)
		if limit["minimum"] != float64(1) || limit["maximum"] != float64(100) || limit["default"] != float64(20) {
			t.Fatalf("%s limit schema=%v", tool.Name, limit)
		}
	}
	listed[0].Annotations["readOnlyHint"] = false
	listed[0].InputSchema[0] = ' '
	listedAgain, _ := tools.ListTools(t.Context(), mcpserver.Principal{})
	if listedAgain[0].Annotations["readOnlyHint"] != true || listedAgain[0].InputSchema[0] != '{' {
		t.Fatal("tool definitions were shared with caller")
	}
}

func TestProgramEventReferenceNativeRequestsAndProjection(t *testing.T) {
	description := "A live performance"
	slug := "sound"
	fixture := &recordingProgramEventReferences{
		types:      []*managev1.ProgramEventType{{Id: managementWorkID, Slug: "concert", Status: managev1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_ACTIVE, RequiresPlace: true, RequiresStreamUrl: true, Locales: []*managev1.ProgramEventTypeLocale{{Locale: "en", Name: "Concert", Description: &description}, {Locale: "ko", Name: "공연"}}}},
		series:     []*managev1.ProgramEventSeries{{Id: managementWorkID, Slug: "first-play", Title: "FIRST PLAY", Status: managev1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_PUBLISHED}},
		labels:     []*managev1.LabelWithStats{{Label: &managev1.Label{Id: managementWorkID, Slug: &slug, Name: "Sound", Status: "LABEL_STATUS_DRAFT", SourceLocale: "en"}}},
		pagination: &commonv1.PaginationResponse{Total: 31, HasMore: true},
	}
	tools := newProgramEventReferenceFixture(t, fixture)
	for _, test := range []struct {
		tool, payload, status, query, titleField, title string
	}{
		{ToolProgramEventTypeList, `{"status":"PROGRAM_EVENT_TYPE_STATUS_ACTIVE","limit":10,"offset":20}`, "PROGRAM_EVENT_TYPE_STATUS_ACTIVE", "", "slug", "concert"},
		{ToolProgramEventSeriesList, `{"query":" FIRST ","status":"PROGRAM_EVENT_SERIES_STATUS_PUBLISHED","limit":10,"offset":20}`, "PROGRAM_EVENT_SERIES_STATUS_PUBLISHED", "FIRST", "title", "FIRST PLAY"},
		{ToolLabelList, `{"query":" Sound ","status":"LABEL_STATUS_DRAFT","limit":10,"offset":20}`, "LABEL_STATUS_DRAFT", "Sound", "name", "Sound"},
	} {
		t.Run(test.tool, func(t *testing.T) {
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, test.tool, toolArguments(t, test.payload))
			if err != nil {
				t.Fatal(err)
			}
			if fixture.requestPagination.Limit != 10 || fixture.requestPagination.Offset != 20 {
				t.Fatal(fixture.requestPagination)
			}
			wantFilters := []*commonv1.FilterSpec{}
			if test.query != "" {
				wantFilters = append(wantFilters, &commonv1.FilterSpec{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: test.query})
			}
			wantFilters = append(wantFilters, &commonv1.FilterSpec{Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: test.status})
			if !reflect.DeepEqual(fixture.filters, wantFilters) {
				t.Fatalf("filters=%v want=%v", fixture.filters, wantFilters)
			}
			output := result.StructuredContent
			if output["total"] != float64(31) || output["limit"] != float64(10) || output["offset"] != float64(20) || output["has_more"] != true || output["next_offset"] != float64(30) {
				t.Fatal(output)
			}
			item := output["items"].([]any)[0].(map[string]any)
			if item["id"] != managementWorkID || item[test.titleField] != test.title || item["status"] != test.status {
				t.Fatal(item)
			}
			if test.tool == ToolProgramEventTypeList {
				locales := item["locales"].([]any)
				if item["requires_place"] != true || item["requires_stream_url"] != true || len(locales) != 2 || locales[0].(map[string]any)["description"] != description || locales[1].(map[string]any)["name"] != "공연" {
					t.Fatal(item)
				}
			}
			if test.tool == ToolLabelList && item["source_locale"] != "en" {
				t.Fatal(item)
			}
			var text map[string]any
			if err := json.Unmarshal([]byte(result.Content[0]["text"].(string)), &text); err != nil || !reflect.DeepEqual(text, output) {
				t.Fatalf("text/structured mismatch: %v", err)
			}
		})
	}
}

func TestProgramEventReferencePaginationAndInputBounds(t *testing.T) {
	for _, name := range []string{ToolProgramEventTypeList, ToolProgramEventSeriesList, ToolLabelList} {
		for _, test := range []struct {
			payload       string
			limit, offset int32
			invalid       bool
		}{
			{payload: `{}`, limit: 20},
			{payload: `{"limit":1}`, limit: 1},
			{payload: `{"limit":100,"offset":2147483647}`, limit: 100, offset: 2147483647},
			{payload: `{"limit":0}`, invalid: true},
			{payload: `{"limit":101}`, invalid: true},
			{payload: `{"limit":-1}`, invalid: true},
			{payload: `{"limit":null}`, invalid: true},
			{payload: `{"offset":null}`, invalid: true},
			{payload: `{"offset":-1}`, invalid: true},
			{payload: `{"offset":2147483648}`, invalid: true},
			{payload: `{"status":null}`, invalid: true},
			{payload: `{"status":""}`, invalid: true},
			{payload: `{"status":"published"}`, invalid: true},
			{payload: `{"query":null}`, invalid: true},
			{payload: `{"extra":true}`, invalid: true},
		} {
			t.Run(name+test.payload, func(t *testing.T) {
				fixture := &recordingProgramEventReferences{}
				tools := newProgramEventReferenceFixture(t, fixture)
				result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, name, toolArguments(t, test.payload))
				if test.invalid {
					var executionErr *mcpserver.ToolExecutionError
					if !errors.As(err, &executionErr) || fixture.calls != 0 {
						t.Fatalf("err=%v calls=%d", err, fixture.calls)
					}
					return
				}
				if err != nil || fixture.calls != 1 || fixture.requestPagination.Limit != test.limit || fixture.requestPagination.Offset != test.offset || len(fixture.filters) != 0 {
					t.Fatalf("err=%v request=%v filters=%v", err, fixture.requestPagination, fixture.filters)
				}
				if result.StructuredContent["has_more"] != false || len(result.StructuredContent["items"].([]any)) != 0 {
					t.Fatal(result.StructuredContent)
				}
				if _, exists := result.StructuredContent["next_offset"]; exists {
					t.Fatal("complete page has a continuation")
				}
			})
		}
	}
	fixture := &recordingProgramEventReferences{}
	tools := newProgramEventReferenceFixture(t, fixture)
	_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolProgramEventTypeList, toolArguments(t, `{"query":"concert"}`))
	if err == nil || fixture.calls != 0 {
		t.Fatalf("unsupported type query reached application: err=%v calls=%d", err, fixture.calls)
	}
	fixture.pagination = &commonv1.PaginationResponse{HasMore: true}
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolProgramEventSeriesList, toolArguments(t, `{"offset":2147483640,"limit":20}`))
	if err != nil || result.StructuredContent["next_offset"] != float64(2147483660) {
		t.Fatalf("continuation wrapped: result=%v err=%v", result.StructuredContent, err)
	}
}

func TestProgramEventReferenceAuthorityErrorsAndDependencies(t *testing.T) {
	fixture := &recordingProgramEventReferences{}
	var typedNil *recordingProgramEventReferences
	for _, dependencies := range []struct {
		types  ProgramEventTypeReferenceDiscovery
		series ProgramEventSeriesReferenceDiscovery
		labels LabelReferenceDiscovery
	}{
		{nil, fixture, fixture}, {fixture, nil, fixture}, {fixture, fixture, nil},
		{typedNil, fixture, fixture}, {fixture, typedNil, fixture}, {fixture, fixture, typedNil},
	} {
		if _, err := NewProgramEventReferenceTools(dependencies.types, dependencies.series, dependencies.labels); err == nil {
			t.Fatal("nil dependency accepted")
		}
	}
	for _, name := range []string{ToolProgramEventTypeList, ToolProgramEventSeriesList, ToolLabelList} {
		for _, code := range []connect.Code{connect.CodePermissionDenied, connect.CodeUnauthenticated, connect.CodeInvalidArgument} {
			fixture := &recordingProgramEventReferences{err: connect.NewError(code, errors.New("native rejection"))}
			tools := newProgramEventReferenceFixture(t, fixture)
			_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, name, toolArguments(t, `{}`))
			var executionErr *mcpserver.ToolExecutionError
			if !errors.As(err, &executionErr) || executionErr.Message != "native rejection" || fixture.calls != 1 {
				t.Fatalf("%s err=%v calls=%d", name, err, fixture.calls)
			}
		}
	}
	tools := newProgramEventReferenceFixture(t, fixture)
	if _, err := tools.CallTool(t.Context(), mcpserver.Principal{}, "unknown", toolArguments(t, `{"limit":null}`)); !errors.Is(err, mcpserver.ErrUnknownTool) {
		t.Fatal(err)
	}
	internalErr := connect.NewError(connect.CodeInternal, errors.New("private failure"))
	fixture.err = internalErr
	if _, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolLabelList, toolArguments(t, `{}`)); !errors.Is(err, internalErr) {
		t.Fatalf("internal failure became a public tool message: %v", err)
	}
}

func newProgramEventReferenceFixture(t *testing.T, fixture *recordingProgramEventReferences) *ProgramEventReferenceTools {
	t.Helper()
	tools, err := NewProgramEventReferenceTools(fixture, fixture, fixture)
	if err != nil {
		t.Fatal(err)
	}
	return tools
}

type recordingProgramEventReferences struct {
	types             []*managev1.ProgramEventType
	series            []*managev1.ProgramEventSeries
	labels            []*managev1.LabelWithStats
	pagination        *commonv1.PaginationResponse
	requestPagination *commonv1.PaginationRequest
	filters           []*commonv1.FilterSpec
	calls             int
	err               error
}

func (r *recordingProgramEventReferences) record(pagination *commonv1.PaginationRequest, filters []*commonv1.FilterSpec) {
	r.calls++
	r.requestPagination = pagination
	r.filters = filters
}

func (r *recordingProgramEventReferences) ListProgramEventTypesAdmin(_ context.Context, request *connect.Request[managev1.ListProgramEventTypesAdminRequest]) (*connect.Response[managev1.ListProgramEventTypesAdminResponse], error) {
	r.record(request.Msg.Pagination, request.Msg.Filters)
	return connect.NewResponse(&managev1.ListProgramEventTypesAdminResponse{Types: r.types, Pagination: r.pagination}), r.err
}

func (r *recordingProgramEventReferences) ListProgramEventSeriesAdmin(_ context.Context, request *connect.Request[managev1.ListProgramEventSeriesAdminRequest]) (*connect.Response[managev1.ListProgramEventSeriesAdminResponse], error) {
	r.record(request.Msg.Pagination, request.Msg.Filters)
	return connect.NewResponse(&managev1.ListProgramEventSeriesAdminResponse{Series: r.series, Pagination: r.pagination}), r.err
}

func (r *recordingProgramEventReferences) ListLabelsAdmin(_ context.Context, request *connect.Request[managev1.ListLabelsAdminRequest]) (*connect.Response[managev1.ListLabelsAdminResponse], error) {
	r.record(request.Msg.Pagination, request.Msg.Filters)
	return connect.NewResponse(&managev1.ListLabelsAdminResponse{Labels: r.labels, Pagination: r.pagination}), r.err
}
