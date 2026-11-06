package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"testing"

	"connectrpc.com/connect"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	memberdomain "github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/programevent"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"github.com/stretchr/testify/require"
)

func allReferenceApplications() ReferenceSearchApplications {
	music, events := &recordingMusicReferences{}, &recordingProgramEventReferences{}
	return ReferenceSearchApplications{
		Categories: &recordingCategoryReferences{}, Tags: &recordingTagReferences{}, Clients: &recordingClientReferences{},
		MapPlaces: &recordingMapPlaceReferences{}, Members: &recordingMemberReferences{}, Artists: &recordingArtistReferences{},
		Genres: music, Styles: music, Formats: music, EventTypes: events, EventSeries: events, MemberTags: &recordingMemberTagReader{},
	}
}

// referenceSearchOver builds the tool over the given lists, leaving the others to
// recorders.
func referenceSearchOver(t *testing.T, given ReferenceSearchApplications) *ReferenceSearchTools {
	t.Helper()
	applications := allReferenceApplications()
	selected, defaults := reflect.ValueOf(given), reflect.ValueOf(&applications).Elem()
	for field := range selected.NumField() {
		if !selected.Field(field).IsNil() {
			defaults.Field(field).Set(selected.Field(field))
		}
	}
	tools, err := NewReferenceSearchTools(applications)
	require.NoError(t, err)
	return tools
}

func callReferenceSearch(t *testing.T, tools *ReferenceSearchTools, referenceType, arguments string) (mcpserver.ToolResult, error) {
	t.Helper()
	decoded := toolArguments(t, arguments)
	decoded["reference_type"] = json.RawMessage(strconv.Quote(referenceType))
	return tools.CallTool(t.Context(), mcpserver.Principal{}, ToolReferenceSearch, decoded)
}

func TestReferenceSearchNeedsEveryApplication(t *testing.T) {
	fields := reflect.TypeFor[ReferenceSearchApplications]().NumField()
	for field := range fields {
		applications := allReferenceApplications()
		reflect.ValueOf(&applications).Elem().Field(field).SetZero()
		_, err := NewReferenceSearchTools(applications)
		require.Error(t, err, reflect.TypeFor[ReferenceSearchApplications]().Field(field).Name)
	}
	applications := allReferenceApplications()
	var typedNil *recordingMemberTagReader
	applications.MemberTags = typedNil
	_, err := NewReferenceSearchTools(applications)
	require.Error(t, err)
}

func TestReferenceSearchRefusesAnUnknownType(t *testing.T) {
	_, err := callReferenceSearch(t, referenceSearchOver(t, ReferenceSearchApplications{}), "unknown", `{"query":"a"}`)
	var execution *mcpserver.ToolExecutionError
	require.ErrorAs(t, err, &execution)
}

// A paged list answers in the shape of every other lookup, without the paging
// details a caller does not pass back.
func TestPagedReferenceListHasTheShapeOfEveryLookup(t *testing.T) {
	music := &recordingMusicReferences{
		genres:     []referencecatalog.ReferenceUsage{{Reference: referencecatalog.Reference{ID: "g", Name: "Techno"}}},
		pagination: referencecatalog.Pagination{Total: 9, HasMore: true},
	}
	result, err := callReferenceSearch(t, referenceSearchOver(t, ReferenceSearchApplications{Genres: music}), "genre", `{"query":"tech","limit":1}`)
	require.NoError(t, err)
	require.Equal(t, "genre", result.StructuredContent["reference_type"])
	require.EqualValues(t, 1, result.StructuredContent["count"])
	require.EqualValues(t, 9, result.StructuredContent["total"])
	require.EqualValues(t, 1, result.StructuredContent["next_offset"])
	require.NotContains(t, result.StructuredContent, "limit")
	require.NotContains(t, result.StructuredContent, "offset")
}

func TestReferenceSearchReachesEveryPagedList(t *testing.T) {
	music, events, tags := &recordingMusicReferences{}, &recordingProgramEventReferences{}, &recordingMemberTagReader{}
	tools := referenceSearchOver(t, ReferenceSearchApplications{Genres: music, Styles: music, Formats: music, EventTypes: events, EventSeries: events, MemberTags: tags})
	for referenceType, calls := range map[string]func() int{
		"genre": func() int { return music.calls }, "style": func() int { return music.calls }, "format": func() int { return music.calls },
		"program_event_type": func() int { return events.calls }, "program_event_series": func() int { return events.calls },
		"member_tag": func() int { return tags.calls },
	} {
		before := calls()
		_, err := callReferenceSearch(t, tools, referenceType, `{}`)
		require.NoError(t, err, referenceType)
		require.Equal(t, before+1, calls(), referenceType)
	}
}

func TestQuickReferenceSearchHasNoOffset(t *testing.T) {
	tools := referenceSearchOver(t, ReferenceSearchApplications{})
	for _, arguments := range []string{`{"query":"a","offset":5}`, `{"query":"a","status":"x"}`, `{}`, `{"query":"  "}`} {
		_, err := callReferenceSearch(t, tools, "artist", arguments)
		var execution *mcpserver.ToolExecutionError
		require.ErrorAs(t, err, &execution, arguments)
	}
}

func TestMusicReferenceRequestsProjectionAndContinuation(t *testing.T) {
	description := "Electronic music"
	emptyDescription := ""
	fixture := &recordingMusicReferences{
		genres:     []referencecatalog.ReferenceUsage{{Reference: referencecatalog.Reference{ID: managementWorkID, Name: "Electronic", Slug: "electronic", Description: &description}, Count: 99}},
		styles:     []referencecatalog.ReferenceUsage{{Reference: referencecatalog.Reference{ID: managementWorkID, Name: "Techno", Slug: "techno", Description: &emptyDescription}}},
		formats:    []referencecatalog.ReferenceUsage{{Reference: referencecatalog.Reference{ID: managementWorkID, Name: "Vinyl", Slug: "vinyl"}}},
		pagination: referencecatalog.Pagination{Total: 25, HasMore: true},
	}
	tools := newMusicReferenceFixture(t, fixture)
	for _, test := range []struct{ name, title, slug string }{
		{"genre", "Electronic", "electronic"}, {"style", "Techno", "techno"}, {"format", "Vinyl", "vinyl"},
	} {
		result, err := callReferenceSearch(t, tools, test.name, `{"query":" music ","limit":10,"offset":10}`)
		if err != nil {
			t.Fatal(err)
		}
		wantFilters := []*commonv1.FilterSpec{{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "music"}}
		if fixture.request.Limit != 10 || fixture.request.Offset != 10 || !reflect.DeepEqual(wantFilters, fixture.request.Filters) {
			t.Fatalf("request=%v", fixture.request)
		}
		output := result.StructuredContent
		if output["total"] != float64(25) || output["has_more"] != true || output["next_offset"] != float64(20) {
			t.Fatal(output)
		}
		item := output["items"].([]any)[0].(map[string]any)
		if item["id"] != managementWorkID || item["name"] != test.title || item["slug"] != test.slug || item["release_count"] != nil {
			t.Fatal(item)
		}
		if test.name == "genre" && item["description"] != description {
			t.Fatal(item)
		}
		if test.name == "style" && item["description"] != "" {
			t.Fatal("explicit empty description was lost")
		}
		var text map[string]any
		if err := json.Unmarshal([]byte(result.Content[0]["text"].(string)), &text); err != nil || !reflect.DeepEqual(text, output) {
			t.Fatalf("text/structured parity: err=%v text=%v output=%v", err, text, output)
		}
	}
	result, err := callReferenceSearch(t, tools, "genre", `{"offset":2147483640,"limit":20}`)
	if err != nil || result.StructuredContent["next_offset"] != float64(2147483660) {
		t.Fatalf("wrapped continuation: %v err=%v", result.StructuredContent, err)
	}
}

func TestMusicReferenceInputBoundsAndEmptyResults(t *testing.T) {
	for _, name := range []string{"genre", "style", "format"} {
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
				result, err := callReferenceSearch(t, tools, name, test.payload)
				if test.invalid {
					var executionErr *mcpserver.ToolExecutionError
					if !errors.As(err, &executionErr) || fixture.calls != 0 {
						t.Fatalf("invalid input reached native list: err=%v calls=%d", err, fixture.calls)
					}
					return
				}
				if err != nil || fixture.calls != 1 || fixture.request.Limit != test.limit || fixture.request.Offset != test.offset || len(fixture.request.Filters) != 0 {
					t.Fatalf("request=%v err=%v", fixture.request, err)
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

func TestMusicReferenceAuthority(t *testing.T) {
	for _, name := range []string{"genre", "style", "format"} {
		for _, code := range []connect.Code{connect.CodePermissionDenied, connect.CodeUnauthenticated, connect.CodeInvalidArgument} {
			fixture := &recordingMusicReferences{err: connect.NewError(code, errors.New("native rejection"))}
			_, err := callReferenceSearch(t, newMusicReferenceFixture(t, fixture), name, `{}`)
			var executionErr *mcpserver.ToolExecutionError
			if !errors.As(err, &executionErr) || executionErr.Message != "native rejection" || fixture.calls != 1 {
				t.Fatalf("%s err=%v calls=%d", name, err, fixture.calls)
			}
		}
	}
	fixture := &recordingMusicReferences{}
	tools := newMusicReferenceFixture(t, fixture)
	internalErr := connect.NewError(connect.CodeInternal, errors.New("private failure"))
	fixture.err = internalErr
	if _, err := callReferenceSearch(t, tools, "genre", `{}`); !errors.Is(err, internalErr) {
		t.Fatalf("private error was converted to public tool message: %v", err)
	}
}

func TestMusicReferenceNativeOwnersRejectDirectUnauthenticatedCalls(t *testing.T) {
	// These dependencies have no connection. The native owners must reject
	// before a database query or SpiceDB call, even without an RPC interceptor.
	catalog := &referencecatalog.Service{}
	tools := referenceSearchOver(t, ReferenceSearchApplications{Genres: catalog, Styles: catalog, Formats: catalog})
	for _, name := range []string{"genre", "style", "format"} {
		_, err := callReferenceSearch(t, tools, name, `{}`)
		var executionErr *mcpserver.ToolExecutionError
		if !errors.As(err, &executionErr) {
			t.Fatalf("%s did not reject direct unauthenticated call: %v", name, err)
		}
	}
}

func newMusicReferenceFixture(t *testing.T, fixture *recordingMusicReferences) *ReferenceSearchTools {
	t.Helper()
	return referenceSearchOver(t, ReferenceSearchApplications{Genres: fixture, Styles: fixture, Formats: fixture})
}

type recordingMusicReferences struct {
	genres     []referencecatalog.ReferenceUsage
	styles     []referencecatalog.ReferenceUsage
	formats    []referencecatalog.ReferenceUsage
	pagination referencecatalog.Pagination
	request    referencecatalog.ReferenceListQuery
	calls      int
	err        error
}

func (r *recordingMusicReferences) record(query referencecatalog.ReferenceListQuery) {
	r.calls++
	r.request = query
}

func (r *recordingMusicReferences) ListGenresAdmin(_ context.Context, query referencecatalog.ReferenceListQuery) (referencecatalog.Page[referencecatalog.ReferenceUsage], error) {
	r.record(query)
	return referencecatalog.Page[referencecatalog.ReferenceUsage]{Items: r.genres, Pagination: r.pagination}, r.err
}

func (r *recordingMusicReferences) ListStylesAdmin(_ context.Context, query referencecatalog.ReferenceListQuery) (referencecatalog.Page[referencecatalog.ReferenceUsage], error) {
	r.record(query)
	return referencecatalog.Page[referencecatalog.ReferenceUsage]{Items: r.styles, Pagination: r.pagination}, r.err
}

func (r *recordingMusicReferences) ListFormatsAdmin(_ context.Context, query referencecatalog.ReferenceListQuery) (referencecatalog.Page[referencecatalog.ReferenceUsage], error) {
	r.record(query)
	return referencecatalog.Page[referencecatalog.ReferenceUsage]{Items: r.formats, Pagination: r.pagination}, r.err
}

func TestProgramEventReferenceNativeRequestsAndProjection(t *testing.T) {
	description := "A live performance"
	fixture := &recordingProgramEventReferences{
		types:      []programevent.TypeView{{Type: programevent.Type{ID: managementWorkID, Slug: "concert", Status: programevent.TypeStatusActive, RequiresPlace: true, RequiresStreamURL: true}, Locales: []programevent.TypeLocale{{Locale: "en", Name: "Concert", Description: &description}, {Locale: "ko", Name: "공연"}}}},
		series:     []programevent.Series{{ID: managementWorkID, Slug: "first-play", Title: "FIRST PLAY", Status: programevent.StatusPublished}},
		pagination: &commonv1.PaginationResponse{Total: 31, HasMore: true},
	}
	tools := newProgramEventReferenceFixture(t, fixture)
	for _, test := range []struct {
		tool, payload, status, query, titleField, title string
	}{
		{"program_event_type", `{"status":"PROGRAM_EVENT_TYPE_STATUS_ACTIVE","limit":10,"offset":20}`, "PROGRAM_EVENT_TYPE_STATUS_ACTIVE", "", "slug", "concert"},
		{"program_event_series", `{"query":" FIRST ","status":"PROGRAM_EVENT_SERIES_STATUS_PUBLISHED","limit":10,"offset":20}`, "PROGRAM_EVENT_SERIES_STATUS_PUBLISHED", "FIRST", "title", "FIRST PLAY"},
	} {
		t.Run(test.tool, func(t *testing.T) {
			result, err := callReferenceSearch(t, tools, test.tool, test.payload)
			if err != nil {
				t.Fatal(err)
			}
			if fixture.requestPagination.Limit != 10 || fixture.requestPagination.Offset != 20 {
				t.Fatal(fixture.requestPagination)
			}
			wantFilters := append(discoverySearchFilters(test.query), &commonv1.FilterSpec{Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: test.status})
			require.Equal(t, wantFilters, fixture.filters)
			output := result.StructuredContent
			if output["total"] != float64(31) || output["has_more"] != true || output["next_offset"] != float64(30) {
				t.Fatal(output)
			}
			item := output["items"].([]any)[0].(map[string]any)
			if item["id"] != managementWorkID || item[test.titleField] != test.title || item["status"] != test.status {
				t.Fatal(item)
			}
			if test.tool == "program_event_type" {
				require.Equal(t, true, item["requires_place"])
				require.Equal(t, true, item["requires_stream_url"])
				require.Equal(t, []any{
					map[string]any{"locale": "en", "name": "Concert", "description": description},
					map[string]any{"locale": "ko", "name": "공연"},
				}, item["locales"])
			}
			var text map[string]any
			require.NoError(t, json.Unmarshal([]byte(result.Content[0]["text"].(string)), &text))
			require.Equal(t, output, text)
		})
	}
}

func TestProgramEventReferencePaginationAndInputBounds(t *testing.T) {
	for _, name := range []string{"program_event_type", "program_event_series"} {
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
				result, err := callReferenceSearch(t, newProgramEventReferenceFixture(t, fixture), name, test.payload)
				if test.invalid {
					var execution *mcpserver.ToolExecutionError
					require.ErrorAs(t, err, &execution)
					require.Zero(t, fixture.calls)
					return
				}
				require.NoError(t, err)
				require.Equal(t, 1, fixture.calls)
				require.Equal(t, test.limit, fixture.requestPagination.Limit)
				require.Equal(t, test.offset, fixture.requestPagination.Offset)
				require.Empty(t, fixture.filters)
				require.Equal(t, false, result.StructuredContent["has_more"])
				require.Empty(t, result.StructuredContent["items"])
				require.NotContains(t, result.StructuredContent, "next_offset")
			})
		}
	}
}

func TestProgramEventTypeListTakesNoQuery(t *testing.T) {
	fixture := &recordingProgramEventReferences{}
	_, err := callReferenceSearch(t, newProgramEventReferenceFixture(t, fixture), "program_event_type", `{"query":"concert"}`)
	require.Error(t, err)
	require.Zero(t, fixture.calls)
}

func TestProgramEventSeriesContinuationDoesNotWrap(t *testing.T) {
	fixture := &recordingProgramEventReferences{pagination: &commonv1.PaginationResponse{HasMore: true}}
	result, err := callReferenceSearch(t, newProgramEventReferenceFixture(t, fixture), "program_event_series", `{"offset":2147483640,"limit":20}`)
	require.NoError(t, err)
	require.Equal(t, float64(2147483660), result.StructuredContent["next_offset"])
}

func TestProgramEventReferenceAuthorityErrors(t *testing.T) {
	for _, name := range []string{"program_event_type", "program_event_series"} {
		for _, code := range []connect.Code{connect.CodePermissionDenied, connect.CodeUnauthenticated, connect.CodeInvalidArgument} {
			fixture := &recordingProgramEventReferences{err: connect.NewError(code, errors.New("native rejection"))}
			tools := newProgramEventReferenceFixture(t, fixture)
			_, err := callReferenceSearch(t, tools, name, `{}`)
			var executionErr *mcpserver.ToolExecutionError
			if !errors.As(err, &executionErr) || executionErr.Message != "native rejection" || fixture.calls != 1 {
				t.Fatalf("%s err=%v calls=%d", name, err, fixture.calls)
			}
		}
	}
	fixture := &recordingProgramEventReferences{}
	tools := newProgramEventReferenceFixture(t, fixture)
	internalErr := connect.NewError(connect.CodeInternal, errors.New("private failure"))
	fixture.err = internalErr
	if _, err := callReferenceSearch(t, tools, "program_event_series", `{}`); !errors.Is(err, internalErr) {
		t.Fatalf("internal failure became a public tool message: %v", err)
	}
}

func newProgramEventReferenceFixture(t *testing.T, fixture *recordingProgramEventReferences) *ReferenceSearchTools {
	t.Helper()
	return referenceSearchOver(t, ReferenceSearchApplications{EventTypes: fixture, EventSeries: fixture})
}

type recordingProgramEventReferences struct {
	types             []programevent.TypeView
	series            []programevent.Series
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

func (r *recordingProgramEventReferences) ListProgramEventTypesAdmin(_ context.Context, query programevent.ListProgramEventTypesQuery) (programevent.TypeList, error) {
	r.record(&commonv1.PaginationRequest{Limit: query.Limit, Offset: query.Offset}, query.Filters)
	return programevent.TypeList{Types: r.types, Pagination: r.programEventPagination()}, r.err
}

func (r *recordingProgramEventReferences) ListProgramEventSeriesAdmin(_ context.Context, query programevent.ListProgramEventSeriesQuery) (programevent.SeriesList, error) {
	r.record(&commonv1.PaginationRequest{Limit: query.Limit, Offset: query.Offset}, query.Filters)
	return programevent.SeriesList{Series: r.series, Pagination: r.programEventPagination()}, r.err
}

func (r *recordingProgramEventReferences) programEventPagination() programevent.Pagination {
	return programevent.Pagination{
		Total: r.pagination.GetTotal(), Limit: r.pagination.GetLimit(), Offset: r.pagination.GetOffset(), HasMore: r.pagination.GetHasMore(),
	}
}

func TestMemberTagListForwardsSupportedSearchAndNativePagination(t *testing.T) {
	for _, test := range []struct {
		arguments string
		limit     int32
		offset    int32
		query     string
		hasMore   bool
	}{
		{arguments: `{}`, limit: 20},
		{arguments: `{"query":"  patrons  ","limit":100,"offset":2147483647}`, limit: 100, offset: 2147483647, query: "patrons", hasMore: true},
	} {
		t.Run(test.arguments, func(t *testing.T) {
			reader := &recordingMemberTagReader{response: memberdomain.TagPage{
				Tags:       []memberdomain.Tag{{ID: memberAdminTestID, Name: "Patrons", MemberCount: 3}},
				Pagination: memberdomain.Pagination{Total: 7, Limit: test.limit, Offset: test.offset, HasMore: test.hasMore},
			}}
			tools := referenceSearchOver(t, ReferenceSearchApplications{MemberTags: reader})
			result, err := callReferenceSearch(t, tools, "member_tag", test.arguments)
			require.NoError(t, err)
			require.Equal(t, t.Context(), reader.ctx)
			require.Equal(t, test.limit, reader.request.Limit)
			require.Equal(t, test.offset, reader.request.Offset)
			require.Equal(t, discoverySearchFilters(test.query), reader.request.Filters)
			require.Empty(t, reader.request.Sorts)
			require.Equal(t, float64(7), result.StructuredContent["total"])
			require.Equal(t, test.hasMore, result.StructuredContent["has_more"])
			if test.hasMore {
				require.Equal(t, float64(int64(test.offset)+int64(test.limit)), result.StructuredContent["next_offset"])
			} else {
				require.NotContains(t, result.StructuredContent, "next_offset")
			}
			item := result.StructuredContent["items"].([]any)[0].(map[string]any)
			require.Equal(t, map[string]any{"id": memberAdminTestID, "name": "Patrons", "member_count": float64(3)}, item)
		})
	}
}

func TestMemberTagListRejectsUnsupportedArgumentsBeforeReading(t *testing.T) {
	for _, arguments := range []string{`{"limit":0}`, `{"limit":101}`, `{"limit":null}`, `{"limit":1.5}`, `{"offset":-1}`, `{"offset":2147483648}`, `{"offset":null}`, `{"query":null}`, `{"type":"private"}`, `{"member_id":"` + memberAdminTestID + `"}`} {
		t.Run(arguments, func(t *testing.T) {
			reader := &recordingMemberTagReader{}
			tools := referenceSearchOver(t, ReferenceSearchApplications{MemberTags: reader})
			_, err := callReferenceSearch(t, tools, "member_tag", arguments)
			var execution *mcpserver.ToolExecutionError
			require.ErrorAs(t, err, &execution)
			require.Zero(t, reader.calls)
		})
	}
}

func TestMemberTagListRetainsNativeAuthenticationAndDenial(t *testing.T) {
	tools := referenceSearchOver(t, ReferenceSearchApplications{MemberTags: &memberdomain.Service{}})
	result, err := callReferenceSearch(t, tools, "member_tag", `{}`)
	var execution *mcpserver.ToolExecutionError
	require.ErrorAs(t, err, &execution)
	require.Empty(t, result.Content)
	require.Nil(t, result.StructuredContent)

	reader := &recordingMemberTagReader{err: connect.NewError(connect.CodePermissionDenied, errors.New("Administrator permission required"))}
	tools = referenceSearchOver(t, ReferenceSearchApplications{MemberTags: reader})
	result, err = callReferenceSearch(t, tools, "member_tag", `{}`)
	require.ErrorAs(t, err, &execution)
	require.Equal(t, "Administrator permission required", execution.Message)
	require.Nil(t, result.StructuredContent)
	require.Equal(t, 1, reader.calls)
}

type recordingMemberTagReader struct {
	response memberdomain.TagPage
	request  memberdomain.ListMemberTagsQuery
	ctx      context.Context
	err      error
	calls    int
}

func (reader *recordingMemberTagReader) ListMemberTags(ctx context.Context, query memberdomain.ListMemberTagsQuery) (memberdomain.TagPage, error) {
	reader.ctx, reader.request = ctx, query
	reader.calls++
	return reader.response, reader.err
}
