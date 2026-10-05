package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

func TestMusicReferenceDescriptors(t *testing.T) {
	tools := newMusicReferenceFixture(t, &recordingMusicReferences{})
	listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	if err != nil || len(listed) != 3 || !reflect.DeepEqual(tools.ToolNames(), []string{ToolGenreList, ToolStyleList, ToolFormatList}) {
		t.Fatalf("tools=%v err=%v", listed, err)
	}
	for _, tool := range listed {
		assertMCPToolOAuthSecurity(t, tool)
		if tool.Annotations["readOnlyHint"] != true || tool.Annotations["destructiveHint"] != false || tool.Annotations["openWorldHint"] != false {
			t.Fatalf("%s annotations=%v", tool.Name, tool.Annotations)
		}
		var input, output map[string]any
		if err := json.Unmarshal(tool.InputSchema, &input); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(tool.OutputSchema, &output); err != nil {
			t.Fatal(err)
		}
		if input["type"] != "object" || input["additionalProperties"] != false || output["type"] != "object" || output["additionalProperties"] != false {
			t.Fatalf("%s schemas=%v %v", tool.Name, input, output)
		}
		properties := input["properties"].(map[string]any)
		if len(properties) != 3 || properties["query"] == nil || properties["limit"] == nil || properties["offset"] == nil {
			t.Fatalf("%s advertised unsupported filters: %v", tool.Name, properties)
		}
		limit := properties["limit"].(map[string]any)
		if limit["minimum"] != float64(1) || limit["maximum"] != float64(100) || limit["default"] != float64(20) {
			t.Fatal(limit)
		}
		item := output["properties"].(map[string]any)["items"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
		if item["id"].(map[string]any)["format"] != "uuid" || item["name"] == nil || item["slug"] == nil {
			t.Fatal(item)
		}
		for _, absent := range []string{"source_locale", "locales", "status", "genre_id", "parent_id"} {
			if item[absent] != nil {
				t.Fatalf("%s advertises unavailable %s", tool.Name, absent)
			}
		}
		_, hasDescription := item["description"]
		if hasDescription != (tool.Name != ToolFormatList) {
			t.Fatalf("%s description advertised=%v", tool.Name, hasDescription)
		}
	}
	listed[0].Annotations["readOnlyHint"] = false
	listed[0].OutputSchema[0] = ' '
	again, _ := tools.ListTools(t.Context(), mcpserver.Principal{})
	if again[0].Annotations["readOnlyHint"] != true || again[0].OutputSchema[0] != '{' {
		t.Fatal("caller mutated shared descriptors")
	}
}

func TestMusicReferenceRequestsProjectionAndContinuation(t *testing.T) {
	description := "Electronic music"
	emptyDescription := ""
	fixture := &recordingMusicReferences{
		genres:     []*managev1.GenreWithStats{{Genre: &managev1.Genre{Id: managementWorkID, Name: "Electronic", Slug: "electronic", Description: &description}, ReleaseCount: 99}},
		styles:     []*managev1.StyleWithStats{{Style: &managev1.Style{Id: managementWorkID, Name: "Techno", Slug: "techno", Description: &emptyDescription}}},
		formats:    []*managev1.FormatWithStats{{Format: &managev1.Format{Id: managementWorkID, Name: "Vinyl", Slug: "vinyl"}}},
		pagination: &commonv1.PaginationResponse{Total: 25, HasMore: true},
	}
	tools := newMusicReferenceFixture(t, fixture)
	for _, test := range []struct{ name, title, slug string }{
		{ToolGenreList, "Electronic", "electronic"}, {ToolStyleList, "Techno", "techno"}, {ToolFormatList, "Vinyl", "vinyl"},
	} {
		result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, test.name, toolArguments(t, `{"query":" music ","limit":10,"offset":10}`))
		if err != nil {
			t.Fatal(err)
		}
		wantFilters := []*commonv1.FilterSpec{{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "music"}}
		if fixture.requestPagination.Limit != 10 || fixture.requestPagination.Offset != 10 || !reflect.DeepEqual(wantFilters, fixture.filters) {
			t.Fatalf("request=%v filters=%v", fixture.requestPagination, fixture.filters)
		}
		output := result.StructuredContent
		if output["total"] != float64(25) || output["has_more"] != true || output["next_offset"] != float64(20) || output["limit"] != float64(10) || output["offset"] != float64(10) {
			t.Fatal(output)
		}
		item := output["items"].([]any)[0].(map[string]any)
		if item["id"] != managementWorkID || item["name"] != test.title || item["slug"] != test.slug || item["release_count"] != nil {
			t.Fatal(item)
		}
		if test.name == ToolGenreList && item["description"] != description {
			t.Fatal(item)
		}
		if test.name == ToolStyleList && item["description"] != "" {
			t.Fatal("explicit empty description was lost")
		}
		var text map[string]any
		if err := json.Unmarshal([]byte(result.Content[0]["text"].(string)), &text); err != nil || !reflect.DeepEqual(text, output) {
			t.Fatalf("text/structured parity: err=%v text=%v output=%v", err, text, output)
		}
	}
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolGenreList, toolArguments(t, `{"offset":2147483640,"limit":20}`))
	if err != nil || result.StructuredContent["next_offset"] != float64(2147483660) {
		t.Fatalf("wrapped continuation: %v err=%v", result.StructuredContent, err)
	}
}

func TestMusicReferenceInputBoundsAndEmptyResults(t *testing.T) {
	for _, name := range []string{ToolGenreList, ToolStyleList, ToolFormatList} {
		for _, test := range []struct {
			payload       string
			limit, offset int32
			invalid       bool
		}{
			{payload: `{}`, limit: 20},
			{payload: `{"query":"  ","limit":1}`, limit: 1},
			{payload: `{"limit":100,"offset":2147483647}`, limit: 100, offset: 2147483647},
			{payload: `{"limit":0}`, invalid: true}, {payload: `{"limit":101}`, invalid: true},
			{payload: `{"limit":-1}`, invalid: true}, {payload: `{"limit":1.5}`, invalid: true},
			{payload: `{"limit":null}`, invalid: true}, {payload: `{"offset":null}`, invalid: true},
			{payload: `{"offset":-1}`, invalid: true}, {payload: `{"offset":2147483648}`, invalid: true},
			{payload: `{"query":null}`, invalid: true}, {payload: `{"query":false}`, invalid: true},
			{payload: `{"status":"published"}`, invalid: true}, {payload: `{"locale":"en"}`, invalid: true},
			{payload: `{"genre_id":"11111111-1111-4111-8111-111111111111"}`, invalid: true},
		} {
			t.Run(name+test.payload, func(t *testing.T) {
				fixture := &recordingMusicReferences{}
				tools := newMusicReferenceFixture(t, fixture)
				result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, name, toolArguments(t, test.payload))
				if test.invalid {
					var executionErr *mcpserver.ToolExecutionError
					if !errors.As(err, &executionErr) || fixture.calls != 0 {
						t.Fatalf("invalid input reached native list: err=%v calls=%d", err, fixture.calls)
					}
					return
				}
				if err != nil || fixture.calls != 1 || fixture.requestPagination.Limit != test.limit || fixture.requestPagination.Offset != test.offset || len(fixture.filters) != 0 {
					t.Fatalf("request=%v filters=%v err=%v", fixture.requestPagination, fixture.filters, err)
				}
				if len(result.StructuredContent["items"].([]any)) != 0 || result.StructuredContent["has_more"] != false {
					t.Fatal(result.StructuredContent)
				}
				if _, ok := result.StructuredContent["next_offset"]; ok {
					t.Fatal("complete page advertised continuation")
				}
			})
		}
	}
}

func TestMusicReferenceAuthorityAndDependencies(t *testing.T) {
	fixture := &recordingMusicReferences{}
	var typedNil *recordingMusicReferences
	for _, dependencies := range []struct {
		genres  GenreReferenceDiscovery
		styles  StyleReferenceDiscovery
		formats FormatReferenceDiscovery
	}{
		{nil, fixture, fixture}, {fixture, nil, fixture}, {fixture, fixture, nil},
		{typedNil, fixture, fixture}, {fixture, typedNil, fixture}, {fixture, fixture, typedNil},
	} {
		if _, err := NewMusicReferenceTools(dependencies.genres, dependencies.styles, dependencies.formats); err == nil {
			t.Fatal("nil dependency accepted")
		}
	}
	for _, name := range []string{ToolGenreList, ToolStyleList, ToolFormatList} {
		for _, code := range []connect.Code{connect.CodePermissionDenied, connect.CodeUnauthenticated, connect.CodeInvalidArgument} {
			fixture := &recordingMusicReferences{err: connect.NewError(code, errors.New("native rejection"))}
			_, err := newMusicReferenceFixture(t, fixture).CallTool(t.Context(), mcpserver.Principal{}, name, toolArguments(t, `{}`))
			var executionErr *mcpserver.ToolExecutionError
			if !errors.As(err, &executionErr) || executionErr.Message != "native rejection" || fixture.calls != 1 {
				t.Fatalf("%s err=%v calls=%d", name, err, fixture.calls)
			}
		}
	}
	tools := newMusicReferenceFixture(t, fixture)
	if _, err := tools.CallTool(t.Context(), mcpserver.Principal{}, "unknown", toolArguments(t, `{"limit":null}`)); !errors.Is(err, mcpserver.ErrUnknownTool) {
		t.Fatal(err)
	}
	internalErr := connect.NewError(connect.CodeInternal, errors.New("private failure"))
	fixture.err = internalErr
	if _, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolGenreList, toolArguments(t, `{}`)); !errors.Is(err, internalErr) {
		t.Fatalf("private error was converted to public tool message: %v", err)
	}
}

func TestMusicReferenceNativeOwnersRejectDirectUnauthenticatedCalls(t *testing.T) {
	// These dependencies have no connection. The native owners must reject
	// before a database query or SpiceDB call, even without an RPC interceptor.
	db, spiceDB := &gorm.DB{}, &auth.SpiceDBClient{}
	tools, err := NewMusicReferenceTools(referencecatalog.NewGenreService(db, spiceDB), referencecatalog.NewStyleService(db, spiceDB), referencecatalog.NewFormatService(db, spiceDB))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{ToolGenreList, ToolStyleList, ToolFormatList} {
		_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, name, toolArguments(t, `{}`))
		var executionErr *mcpserver.ToolExecutionError
		if !errors.As(err, &executionErr) {
			t.Fatalf("%s did not reject direct unauthenticated call: %v", name, err)
		}
	}
}

func newMusicReferenceFixture(t *testing.T, fixture *recordingMusicReferences) *MusicReferenceTools {
	t.Helper()
	tools, err := NewMusicReferenceTools(fixture, fixture, fixture)
	if err != nil {
		t.Fatal(err)
	}
	return tools
}

type recordingMusicReferences struct {
	genres            []*managev1.GenreWithStats
	styles            []*managev1.StyleWithStats
	formats           []*managev1.FormatWithStats
	pagination        *commonv1.PaginationResponse
	requestPagination *commonv1.PaginationRequest
	filters           []*commonv1.FilterSpec
	calls             int
	err               error
}

func (r *recordingMusicReferences) record(page *commonv1.PaginationRequest, filters []*commonv1.FilterSpec) {
	r.calls++
	r.requestPagination, r.filters = page, filters
}

func (r *recordingMusicReferences) ListGenresAdmin(_ context.Context, request *connect.Request[managev1.ListGenresAdminRequest]) (*connect.Response[managev1.ListGenresAdminResponse], error) {
	r.record(request.Msg.Pagination, request.Msg.Filters)
	return connect.NewResponse(&managev1.ListGenresAdminResponse{Genres: r.genres, Pagination: r.pagination}), r.err
}

func (r *recordingMusicReferences) ListStylesAdmin(_ context.Context, request *connect.Request[managev1.ListStylesAdminRequest]) (*connect.Response[managev1.ListStylesAdminResponse], error) {
	r.record(request.Msg.Pagination, request.Msg.Filters)
	return connect.NewResponse(&managev1.ListStylesAdminResponse{Styles: r.styles, Pagination: r.pagination}), r.err
}

func (r *recordingMusicReferences) ListFormatsAdmin(_ context.Context, request *connect.Request[managev1.ListFormatsAdminRequest]) (*connect.Response[managev1.ListFormatsAdminResponse], error) {
	r.record(request.Msg.Pagination, request.Msg.Filters)
	return connect.NewResponse(&managev1.ListFormatsAdminResponse{Formats: r.formats, Pagination: r.pagination}), r.err
}
