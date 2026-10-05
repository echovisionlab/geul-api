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

func TestPageReferenceDescriptors(t *testing.T) {
	tools := newPageReferenceFixture(t, &recordingPageReferences{})
	listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	if err != nil || len(listed) != 2 || !reflect.DeepEqual(tools.ToolNames(), []string{ToolFormList, ToolPostSeriesList}) {
		t.Fatalf("tools=%v names=%v err=%v", listed, tools.ToolNames(), err)
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
		limit := properties["limit"].(map[string]any)
		if properties["query"] == nil || properties["status"] == nil || limit["minimum"] != float64(1) || limit["maximum"] != float64(100) || limit["default"] != float64(20) {
			t.Fatalf("%s properties=%v", tool.Name, properties)
		}
	}
	listed[0].Annotations["readOnlyHint"] = false
	listed[0].InputSchema[0] = ' '
	again, _ := tools.ListTools(t.Context(), mcpserver.Principal{})
	if again[0].Annotations["readOnlyHint"] != true || again[0].InputSchema[0] != '{' {
		t.Fatal("tool definitions shared with caller")
	}
}

func TestPageReferenceNativeRequestsAndCompactProjection(t *testing.T) {
	slug := "contact"
	description := "Private native body"
	fixture := &recordingPageReferences{
		forms:      []*managev1.FormSummary{nil, {Id: managementWorkID, Title: "문의 폼", Slug: &slug, Status: managev1.FormStatus_FORM_STATUS_PUBLISHED, SourceLocale: "ko", SubmissionCount: 19}},
		series:     []*managev1.SeriesWithStats{nil, {}, {Series: &managev1.Series{Id: managementWorkID, Title: "소식", Slug: "news", Status: "SERIES_STATUS_PUBLISHED", SourceLocale: "ko", Description: &description}, PostCount: 4, ManagerCount: 7}},
		pagination: &commonv1.PaginationResponse{Total: 31, HasMore: true},
	}
	tools := newPageReferenceFixture(t, fixture)
	for _, test := range []struct {
		tool, payload, title, status string
		wantFields                   int
	}{
		{ToolFormList, `{"query":" 문의 ","status":"FORM_STATUS_PUBLISHED","limit":10,"offset":20}`, "문의 폼", "FORM_STATUS_PUBLISHED", 5},
		{ToolPostSeriesList, `{"query":" 문의 ","status":"SERIES_STATUS_PUBLISHED","limit":10,"offset":20}`, "소식", "SERIES_STATUS_PUBLISHED", 6},
	} {
		t.Run(test.tool, func(t *testing.T) {
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, test.tool, toolArguments(t, test.payload))
			if err != nil {
				t.Fatal(err)
			}
			wantFilters := []*commonv1.FilterSpec{{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "문의"}, {Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: test.status}}
			if fixture.requestPagination.Limit != 10 || fixture.requestPagination.Offset != 20 || !reflect.DeepEqual(fixture.filters, wantFilters) || len(fixture.sorts) != 0 {
				t.Fatalf("pagination=%v filters=%v sorts=%v", fixture.requestPagination, fixture.filters, fixture.sorts)
			}
			output := result.StructuredContent
			if output["total"] != float64(31) || output["limit"] != float64(10) || output["offset"] != float64(20) || output["has_more"] != true || output["next_offset"] != float64(30) {
				t.Fatal(output)
			}
			items := output["items"].([]any)
			if len(items) != 1 {
				t.Fatal(items)
			}
			item := items[0].(map[string]any)
			if len(item) != test.wantFields || item["id"] != managementWorkID || item["title"] != test.title || item["status"] != test.status || item["source_locale"] != "ko" {
				t.Fatal(item)
			}
			if test.tool == ToolPostSeriesList && item["post_count"] != float64(4) {
				t.Fatal(item)
			}
			for _, omitted := range []string{"schema", "description", "submissions", "submission_count", "manager_count", "managers", "created_at", "og_asset"} {
				if _, exists := item[omitted]; exists {
					t.Fatalf("native field %s leaked: %v", omitted, item)
				}
			}
			var text map[string]any
			if err := json.Unmarshal([]byte(result.Content[0]["text"].(string)), &text); err != nil || !reflect.DeepEqual(output, text) {
				t.Fatalf("structured/text disagreement: %v", err)
			}
		})
	}
	fixture.forms[1].Slug = nil
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFormList, toolArguments(t, `{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, supplied := result.StructuredContent["items"].([]any)[0].(map[string]any)["slug"]; supplied {
		t.Fatal("absent native slug was invented")
	}
}

func TestPageReferencePaginationAndInvalidInputs(t *testing.T) {
	for _, name := range []string{ToolFormList, ToolPostSeriesList} {
		for _, test := range []struct {
			payload string
			limit   int32
			invalid bool
		}{
			{payload: `{}`, limit: 20},
			{payload: `{"query":"  ","limit":100}`, limit: 100},
			{payload: `{"limit":0}`, invalid: true},
			{payload: `{"limit":101}`, invalid: true},
			{payload: `{"limit":1.5}`, invalid: true},
			{payload: `{"limit":null}`, invalid: true},
			{payload: `{"offset":-1}`, invalid: true},
			{payload: `{"offset":2147483648}`, invalid: true},
			{payload: `{"offset":null}`, invalid: true},
			{payload: `{"query":null}`, invalid: true},
			{payload: `{"status":null}`, invalid: true},
			{payload: `{"status":""}`, invalid: true},
			{payload: `{"status":"published"}`, invalid: true},
			{payload: `{"status":"FORM_STATUS_UNSPECIFIED"}`, invalid: true},
			{payload: `{"status":"SERIES_STATUS_UNSPECIFIED"}`, invalid: true},
			{payload: `{"extra":true}`, invalid: true},
		} {
			t.Run(name+test.payload, func(t *testing.T) {
				fixture := &recordingPageReferences{}
				tools := newPageReferenceFixture(t, fixture)
				result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, name, toolArguments(t, test.payload))
				if test.invalid {
					var executionErr *mcpserver.ToolExecutionError
					if !errors.As(err, &executionErr) || fixture.calls != 0 {
						t.Fatalf("err=%v calls=%d", err, fixture.calls)
					}
					return
				}
				if err != nil || fixture.calls != 1 || fixture.requestPagination.Limit != test.limit || len(fixture.filters) != 0 {
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
	fixture := &recordingPageReferences{pagination: &commonv1.PaginationResponse{HasMore: true}}
	tools := newPageReferenceFixture(t, fixture)
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolPostSeriesList, toolArguments(t, `{"offset":2147483640,"limit":20}`))
	if err != nil || result.StructuredContent["next_offset"] != float64(2147483660) {
		t.Fatalf("continuation wrapped: result=%v err=%v", result.StructuredContent, err)
	}
}

func TestPageReferenceAuthorityErrorsAndDependencies(t *testing.T) {
	fixture := &recordingPageReferences{}
	var typedNil *recordingPageReferences
	for _, dependencies := range []struct {
		forms  FormReferenceDiscovery
		series PostSeriesReferenceDiscovery
	}{{nil, fixture}, {fixture, nil}, {typedNil, fixture}, {fixture, typedNil}} {
		if _, err := NewPageReferenceTools(dependencies.forms, dependencies.series); err == nil {
			t.Fatal("nil dependency accepted")
		}
	}
	for _, name := range []string{ToolFormList, ToolPostSeriesList} {
		for _, code := range []connect.Code{connect.CodePermissionDenied, connect.CodeUnauthenticated, connect.CodeInvalidArgument} {
			fixture := &recordingPageReferences{err: connect.NewError(code, errors.New("native rejection"))}
			tools := newPageReferenceFixture(t, fixture)
			_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, name, toolArguments(t, `{}`))
			var executionErr *mcpserver.ToolExecutionError
			if !errors.As(err, &executionErr) || executionErr.Message != "native rejection" || fixture.calls != 1 {
				t.Fatalf("%s err=%v calls=%d", name, err, fixture.calls)
			}
		}
	}
	tools := newPageReferenceFixture(t, fixture)
	if _, err := tools.CallTool(t.Context(), mcpserver.Principal{}, "unknown", toolArguments(t, `{"limit":null}`)); !errors.Is(err, mcpserver.ErrUnknownTool) {
		t.Fatal(err)
	}
	internalErr := connect.NewError(connect.CodeInternal, errors.New("private failure"))
	fixture.err = internalErr
	if _, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolFormList, toolArguments(t, `{}`)); !errors.Is(err, internalErr) {
		t.Fatalf("internal failure became a public tool message: %v", err)
	}
}

func newPageReferenceFixture(t *testing.T, fixture *recordingPageReferences) *PageReferenceTools {
	t.Helper()
	tools, err := NewPageReferenceTools(fixture, fixture)
	if err != nil {
		t.Fatal(err)
	}
	return tools
}

type recordingPageReferences struct {
	forms             []*managev1.FormSummary
	series            []*managev1.SeriesWithStats
	pagination        *commonv1.PaginationResponse
	requestPagination *commonv1.PaginationRequest
	filters           []*commonv1.FilterSpec
	sorts             []*commonv1.SortSpec
	calls             int
	err               error
}

func (r *recordingPageReferences) ListFormsAdmin(_ context.Context, request *connect.Request[managev1.ListFormsAdminRequest]) (*connect.Response[managev1.ListFormsAdminResponse], error) {
	r.calls++
	r.requestPagination, r.filters, r.sorts = request.Msg.Pagination, request.Msg.Filters, request.Msg.Sorts
	return connect.NewResponse(&managev1.ListFormsAdminResponse{Forms: r.forms, Pagination: r.pagination}), r.err
}

func (r *recordingPageReferences) ListSeriesAdmin(_ context.Context, request *connect.Request[managev1.ListSeriesAdminRequest]) (*connect.Response[managev1.ListSeriesAdminResponse], error) {
	r.calls++
	r.requestPagination, r.filters, r.sorts = request.Msg.Pagination, request.Msg.Filters, request.Msg.Sorts
	return connect.NewResponse(&managev1.ListSeriesAdminResponse{Series: r.series, Pagination: r.pagination}), r.err
}
