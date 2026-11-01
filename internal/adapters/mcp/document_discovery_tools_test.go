package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/programevent"
	workdomain "github.com/echovisionlab/geul-api/internal/work"
	"github.com/stretchr/testify/require"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/artist"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	pagedomain "github.com/echovisionlab/geul-api/internal/page"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	releasedomain "github.com/echovisionlab/geul-api/internal/release"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const discoveryDocumentID = "44444444-4444-4444-8444-444444444444"

func TestDocumentDiscoveryRejectsPaginationOutsideOwningAPIBounds(t *testing.T) {
	for _, profile := range []string{"post", "work", "page", "program_event", "release", "artist"} {
		for _, pagination := range []string{`"offset":null`, `"offset":-1`, `"offset":2147483648`, `"offset":4294967296`, `"limit":null`, `"limit":0`, `"limit":-1`, `"limit":51`, `"limit":4294967296`} {
			t.Run(profile+"/"+pagination, func(t *testing.T) {
				posts := &recordingPostDocumentDiscovery{}
				releases, artists := &recordingReleaseDocumentDiscovery{}, &recordingArtistDocumentDiscovery{}
				events := &recordingProgramEventDocumentDiscovery{}
				tools, err := NewDocumentDiscoveryTools(posts, &recordingWorkDocumentDiscovery{}, &recordingPageDocumentDiscovery{}, events, releases, artists)
				require.NoError(t, err)
				_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentList, toolArguments(t, `{"p":"`+profile+`",`+pagination+`}`))
				var executionErr *mcpserver.ToolExecutionError
				require.ErrorAs(t, err, &executionErr)
				require.Empty(t, posts.input)
				require.Nil(t, events.request)
				require.Nil(t, releases.request)
				require.Nil(t, artists.request)
			})
		}
	}
}

func TestDocumentDiscoveryPreservesMaximumOwningAPIOffset(t *testing.T) {
	releases := &recordingReleaseDocumentDiscovery{}
	tools, err := NewDocumentDiscoveryTools(&recordingPostDocumentDiscovery{}, &recordingWorkDocumentDiscovery{}, &recordingPageDocumentDiscovery{}, &recordingProgramEventDocumentDiscovery{}, releases, &recordingArtistDocumentDiscovery{})
	require.NoError(t, err)
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentList, toolArguments(t, `{"p":"release","limit":50,"offset":2147483647}`))
	require.NoError(t, err)
	require.Equal(t, int32(2147483647), releases.request.Msg.Pagination.Offset)
	require.Equal(t, int32(50), releases.request.Msg.Pagination.Limit)
	require.Nil(t, result.StructuredContent["next_offset"])
}

func TestDocumentDiscoveryToolDescriptorAndAuthorizedResult(t *testing.T) {
	slug := "test-post"
	updatedAt := time.Date(2026, time.August, 27, 5, 2, 3, 0, time.UTC)
	discovery := &recordingPostDocumentDiscovery{result: postdomain.AIDocumentListResult{
		Items: []postdomain.AIDocumentListItem{{
			ID: discoveryDocumentID, Title: "Test Post", Slug: &slug,
			SourceLocale: "ko", Status: "POST_STATUS_DRAFT", UpdatedAt: updatedAt,
			ConfigurationRevision: managementPostConfigurationRevision,
		}},
		Total: 3, Limit: 1, Offset: 1,
	}}
	tools, err := NewDocumentDiscoveryTools(discovery, &recordingWorkDocumentDiscovery{}, &recordingPageDocumentDiscovery{}, &recordingProgramEventDocumentDiscovery{}, &recordingReleaseDocumentDiscovery{}, &recordingArtistDocumentDiscovery{})
	if err != nil {
		t.Fatalf("NewDocumentDiscoveryTools() error = %v", err)
	}

	listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(listed) != 1 || listed[0].Name != ToolDocumentList {
		t.Fatalf("ListTools() = %#v, want document_list", listed)
	}
	assertMCPToolOAuthSecurity(t, listed[0])
	assertMCPToolAnnotations(t, listed[0], toolAnnotations(true, false, false))
	for name, schema := range map[string]json.RawMessage{
		"input": listed[0].InputSchema, "output": listed[0].OutputSchema,
	} {
		var object map[string]any
		if err := json.Unmarshal(schema, &object); err != nil || object["type"] != "object" {
			t.Fatalf("%s schema is not an object: %s (%v)", name, schema, err)
		}
	}

	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentList, toolArguments(t, `{
		"p":"post","q":"Test","limit":1,"offset":1
	}`))
	if err != nil {
		t.Fatalf("document_list error = %v", err)
	}
	wantInput := postdomain.AIDocumentListInput{Query: "Test", Limit: 1, Offset: 1}
	if !reflect.DeepEqual(discovery.input, wantInput) {
		t.Fatalf("ListAIDocuments input = %#v, want %#v", discovery.input, wantInput)
	}
	documents, ok := result.StructuredContent["documents"].([]any)
	if !ok || len(documents) != 1 {
		t.Fatalf("document_list documents = %#v", result.StructuredContent["documents"])
	}
	document, ok := documents[0].(map[string]any)
	if !ok || document["d"] != discoveryDocumentID || document["slug"] != slug || document["title"] != "Test Post" || document["configuration_revision"] != managementPostConfigurationRevision {
		t.Fatalf("document_list document = %#v", documents[0])
	}
	if result.StructuredContent["next_offset"] != float64(2) || result.StructuredContent["total"] != float64(3) {
		t.Fatalf("document_list pagination = %#v", result.StructuredContent)
	}
}

func TestDocumentDiscoveryToolRejectsUnsupportedProfileAndMapsExpectedErrors(t *testing.T) {
	discovery := &recordingPostDocumentDiscovery{}
	tools, err := NewDocumentDiscoveryTools(discovery, &recordingWorkDocumentDiscovery{}, &recordingPageDocumentDiscovery{}, &recordingProgramEventDocumentDiscovery{}, &recordingReleaseDocumentDiscovery{}, &recordingArtistDocumentDiscovery{})
	if err != nil {
		t.Fatalf("NewDocumentDiscoveryTools() error = %v", err)
	}

	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentList, toolArguments(t, `{"p":"label"}`))
	var executionErr *mcpserver.ToolExecutionError
	if result.Content != nil || !errors.As(err, &executionErr) || !strings.Contains(executionErr.Message, "p must be") {
		t.Fatalf("unsupported profile result = %#v, error = %v", result, err)
	}

	discovery.err = connect.NewError(connect.CodePermissionDenied, errors.New("permission denied"))
	result, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentList, toolArguments(t, `{"p":"post"}`))
	if result.Content != nil || !errors.As(err, &executionErr) || executionErr.Message != "permission denied" {
		t.Fatalf("permission result = %#v, error = %v", result, err)
	}

	discovery.err = errors.New("database included a secret")
	if _, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentList, toolArguments(t, `{"p":"post"}`)); err == nil {
		t.Fatal("document_list hid an internal failure as a safe tool error")
	} else if errors.As(err, &executionErr) {
		t.Fatalf("internal failure was exposed as ToolExecutionError: %v", err)
	}
}

func TestNewDocumentDiscoveryToolsRejectsNil(t *testing.T) {
	if _, err := NewDocumentDiscoveryTools(nil, &recordingWorkDocumentDiscovery{}, &recordingPageDocumentDiscovery{}, &recordingProgramEventDocumentDiscovery{}, &recordingReleaseDocumentDiscovery{}, &recordingArtistDocumentDiscovery{}); err == nil {
		t.Fatal("NewDocumentDiscoveryTools(nil) succeeded")
	}
}

func TestDocumentDiscoveryListsProgramEvents(t *testing.T) {
	updatedAt := time.Date(2026, time.August, 28, 1, 2, 3, 0, time.UTC)
	slug := "live-set"
	programEvents := &recordingProgramEventDocumentDiscovery{list: programevent.EventList{
		Events: []*managev1.ProgramEventSummary{{
			Id: discoveryDocumentID, Title: "Live Set", Slug: &slug, SourceLocale: "ko",
			Status:    managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED,
			UpdatedAt: timestamppb.New(updatedAt),
		}},
		Pagination: &commonv1.PaginationResponse{Total: 1, Limit: 10},
	}}
	tools, err := NewDocumentDiscoveryTools(&recordingPostDocumentDiscovery{}, &recordingWorkDocumentDiscovery{}, &recordingPageDocumentDiscovery{}, programEvents, &recordingReleaseDocumentDiscovery{}, &recordingArtistDocumentDiscovery{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentList, toolArguments(t, `{"p":"program_event","q":"Live","limit":10}`))
	if err != nil {
		t.Fatal(err)
	}
	if programEvents.request == nil || len(programEvents.request.Filters) != 1 || programEvents.request.Filters[0].Value != "Live" || programEvents.request.Limit != 10 {
		t.Fatalf("Program Event request = %+v", programEvents.request)
	}
	documents := result.StructuredContent["documents"].([]any)
	document := documents[0].(map[string]any)
	if document["p"] != "program_event" || document["d"] != discoveryDocumentID || document["status"] != "published" {
		t.Fatalf("Program Event document = %+v", document)
	}
}

type recordingPostDocumentDiscovery struct {
	input  postdomain.AIDocumentListInput
	result postdomain.AIDocumentListResult
	err    error
}

func (discovery *recordingPostDocumentDiscovery) ListAIDocuments(
	_ context.Context,
	input postdomain.AIDocumentListInput,
) (postdomain.AIDocumentListResult, error) {
	discovery.input = input
	return discovery.result, discovery.err
}

type recordingWorkDocumentDiscovery struct{}

func (*recordingWorkDocumentDiscovery) ListWorksAdmin(context.Context, workdomain.ListWorksAdminQuery) (workdomain.AdminWorkPage, error) {
	return workdomain.AdminWorkPage{}, nil
}

type recordingPageDocumentDiscovery struct {
	managev1connect.UnimplementedPageServiceHandler
}

type recordingProgramEventDocumentDiscovery struct {
	request *programevent.ListProgramEventsQuery
	list    programevent.EventList
}

func (discovery *recordingProgramEventDocumentDiscovery) ListProgramEventsAdmin(
	_ context.Context,
	query programevent.ListProgramEventsQuery,
) (programevent.EventList, error) {
	discovery.request = &query
	return discovery.list, nil
}

func (*recordingPageDocumentDiscovery) ListPagesAdmin(
	context.Context,
	pagedomain.ListPagesAdminQuery,
) (*managev1.ListPagesAdminResponse, error) {
	return &managev1.ListPagesAdminResponse{}, nil
}

func TestDocumentDiscoveryReleaseAndArtistRequestsAndMetadata(t *testing.T) {
	for _, profile := range []string{"release", "artist"} {
		t.Run(profile, func(t *testing.T) {
			slug := "same-name-is-not-an-id"
			updatedAt := time.Date(2026, 10, 3, 12, 34, 56, 0, time.FixedZone("KST", 9*60*60))
			releases := &recordingReleaseDocumentDiscovery{response: &managev1.ListReleasesAdminResponse{
				Releases: []*managev1.ReleaseWithStats{nil, {}, {Release: &managev1.Release{}}, {Release: &managev1.Release{
					Id: discoveryDocumentID, Title: "Same Name", Slug: &slug, SourceLocale: "ko", Status: "draft", UpdatedAt: timestamppb.New(updatedAt),
				}}}, Pagination: &commonv1.PaginationResponse{Total: 25, Limit: 2, Offset: 4},
			}}
			artists := &recordingArtistDocumentDiscovery{page: artist.AdminArtistPage{
				Artists: []artist.AdminView{{}, {Artist: artist.View{Name: "Same Name", Artist: artist.Artist{
					ID: discoveryDocumentID, Slug: &slug, SourceLocale: "ko", Status: "published", UpdatedAt: &updatedAt,
				}}}}, Pagination: artist.Pagination{Total: 25, Limit: 2, Offset: 4},
			}}
			tools, err := NewDocumentDiscoveryTools(&recordingPostDocumentDiscovery{}, &recordingWorkDocumentDiscovery{}, &recordingPageDocumentDiscovery{}, &recordingProgramEventDocumentDiscovery{}, releases, artists)
			require.NoError(t, err)
			user := &auth.UserInfo{IdentityID: auth.IdentityID("11111111-1111-4111-8111-111111111111"), MemberID: auth.MemberID("22222222-2222-4222-8222-222222222222")}
			ctx := auth.WithUser(t.Context(), user)
			result, err := tools.CallTool(ctx, mcpserver.Principal{}, ToolDocumentList, toolArguments(t, `{"p":"`+profile+`","q":"  Same Name  ","limit":2,"offset":4}`))
			require.NoError(t, err)
			var pagination *commonv1.PaginationRequest
			var filters []*commonv1.FilterSpec
			var sorts []*commonv1.SortSpec
			status := "draft"
			if profile == "release" {
				require.Same(t, user, auth.GetUser(releases.ctx))
				pagination, filters, sorts = releases.request.Msg.Pagination, releases.request.Msg.Filters, releases.request.Msg.Sorts
				require.Nil(t, artists.request, "release discovery must only call its owning service")
			} else {
				require.Same(t, user, auth.GetUser(artists.ctx))
				pagination, filters, sorts = artists.request.Msg.Pagination, artists.request.Msg.Filters, artists.request.Msg.Sorts
				require.Nil(t, releases.request, "artist discovery must only call its owning service")
				status = "published"
			}
			require.Equal(t, int32(2), pagination.Limit)
			require.Equal(t, int32(4), pagination.Offset)
			require.Len(t, filters, 1)
			require.Equal(t, "search", filters[0].Field)
			require.Equal(t, commonv1.FilterOp_FILTER_OP_ILIKE, filters[0].Op)
			require.Equal(t, "Same Name", filters[0].Value)
			require.Len(t, sorts, 1)
			require.Equal(t, "updated_at", sorts[0].Field)
			require.Equal(t, commonv1.SortOrder_SORT_ORDER_DESC, sorts[0].Order)
			documents := result.StructuredContent["documents"].([]any)
			require.Len(t, documents, 1, "nil wrappers, entities and absent timestamps must be skipped")
			require.Equal(t, map[string]any{
				"p": profile, "d": discoveryDocumentID, "title": "Same Name", "slug": slug,
				"source_locale": "ko", "status": status, "updated_at": updatedAt.UTC().Format(time.RFC3339),
			}, documents[0])
			require.Equal(t, float64(25), result.StructuredContent["total"])
			require.Equal(t, float64(6), result.StructuredContent["next_offset"])

			// A final page uses the default page size and omits an absent slug.
			releases.response.Pagination = &commonv1.PaginationResponse{Total: 1, Limit: 20}
			releases.response.Releases[3].Release.Slug = nil
			artists.page.Pagination = artist.Pagination{Total: 1, Limit: 20}
			artists.page.Artists[1].Artist.Artist.Slug = nil
			result, err = tools.CallTool(ctx, mcpserver.Principal{}, ToolDocumentList, toolArguments(t, `{"p":"`+profile+`","q":"   "}`))
			require.NoError(t, err)
			require.Nil(t, result.StructuredContent["next_offset"])
			document := result.StructuredContent["documents"].([]any)[0].(map[string]any)
			require.NotContains(t, document, "slug")
			if profile == "release" {
				require.Equal(t, int32(20), releases.request.Msg.Pagination.Limit)
				require.Empty(t, releases.request.Msg.Filters)
			} else {
				require.Equal(t, int32(20), artists.request.Msg.Pagination.Limit)
				require.Empty(t, artists.request.Msg.Filters)
			}
		})
	}
}

func TestDocumentDiscoveryNewProfilesSchemasAndAuthorityErrors(t *testing.T) {
	releases, artists := &recordingReleaseDocumentDiscovery{}, &recordingArtistDocumentDiscovery{}
	tools, err := NewDocumentDiscoveryTools(&recordingPostDocumentDiscovery{}, &recordingWorkDocumentDiscovery{}, &recordingPageDocumentDiscovery{}, &recordingProgramEventDocumentDiscovery{}, releases, artists)
	require.NoError(t, err)
	listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	require.NoError(t, err)
	var inputSchema, outputSchema map[string]any
	require.NoError(t, json.Unmarshal(listed[0].InputSchema, &inputSchema))
	require.NoError(t, json.Unmarshal(listed[0].OutputSchema, &outputSchema))
	inputProperties := inputSchema["properties"].(map[string]any)
	outputProperties := outputSchema["properties"].(map[string]any)["documents"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	for _, properties := range []map[string]any{inputProperties, outputProperties} {
		require.ElementsMatch(t, []any{"post", "work", "page", "program_event", "release", "artist"}, properties["p"].(map[string]any)["enum"])
	}
	for _, profile := range []string{"post", "work", "page", "program_event"} {
		_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentList, toolArguments(t, `{"p":"`+profile+`"}`))
		require.NoError(t, err, "existing profile %s must remain discoverable", profile)
	}
	for _, profile := range []string{"release", "artist"} {
		t.Run(profile, func(t *testing.T) {
			denied := connect.NewError(connect.CodePermissionDenied, errors.New("permission denied"))
			releases.err, artists.err = denied, denied
			_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentList, toolArguments(t, `{"p":"`+profile+`"}`))
			var executionErr *mcpserver.ToolExecutionError
			require.ErrorAs(t, err, &executionErr)
			require.Equal(t, "permission denied", executionErr.Message)
			releases.err, artists.err = errors.New("private database detail"), errors.New("private database detail")
			_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentList, toolArguments(t, `{"p":"`+profile+`"}`))
			require.Error(t, err)
			require.False(t, errors.As(err, &executionErr), "internal errors must be left for generic transport handling")
			releases.err, artists.err = nil, nil
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentList, toolArguments(t, `{"p":"`+profile+`"}`))
			require.NoError(t, err, "empty service responses may omit pagination")
			require.Empty(t, result.StructuredContent["documents"])
			require.Nil(t, result.StructuredContent["next_offset"])
		})
	}
}

func TestDocumentDiscoveryRejectsMissingReleaseAndArtistServices(t *testing.T) {
	var nilRelease *recordingReleaseDocumentDiscovery
	var nilArtist *recordingArtistDocumentDiscovery
	for _, services := range []struct {
		release ReleaseDocumentDiscovery
		artist  ArtistDocumentDiscovery
	}{
		{nil, &recordingArtistDocumentDiscovery{}}, {nilRelease, &recordingArtistDocumentDiscovery{}},
		{&recordingReleaseDocumentDiscovery{}, nil}, {&recordingReleaseDocumentDiscovery{}, nilArtist},
	} {
		_, err := NewDocumentDiscoveryTools(&recordingPostDocumentDiscovery{}, &recordingWorkDocumentDiscovery{}, &recordingPageDocumentDiscovery{}, &recordingProgramEventDocumentDiscovery{}, services.release, services.artist)
		require.Error(t, err)
	}
}

type recordingReleaseDocumentDiscovery struct {
	ctx      context.Context
	request  *connect.Request[managev1.ListReleasesAdminRequest]
	response *managev1.ListReleasesAdminResponse
	err      error
}

// ListReleasesAdmin records the query as the Connect request it stands for.
func (discovery *recordingReleaseDocumentDiscovery) ListReleasesAdmin(ctx context.Context, query releasedomain.ListReleasesQuery) (releasedomain.ReleasePage, error) {
	discovery.ctx, discovery.request = ctx, connect.NewRequest(&managev1.ListReleasesAdminRequest{
		Pagination: &commonv1.PaginationRequest{Limit: query.Limit, Offset: query.Offset},
		Filters:    query.Filters, Sorts: query.Sorts,
	})
	if discovery.response == nil {
		discovery.response = &managev1.ListReleasesAdminResponse{}
	}
	return releasedomain.ReleasePage{
		Releases: discovery.response.Releases,
		Total:    int64(discovery.response.Pagination.GetTotal()), Limit: discovery.response.Pagination.GetLimit(),
	}, discovery.err
}

type recordingArtistDocumentDiscovery struct {
	ctx      context.Context
	request *connect.Request[managev1.ListArtistsAdminRequest]
	page    artist.AdminArtistPage
	err     error
}

func (discovery *recordingArtistDocumentDiscovery) ListArtistsAdmin(ctx context.Context, query artist.ListArtistsQuery) (artist.AdminArtistPage, error) {
	discovery.ctx = ctx
	discovery.request = connect.NewRequest(&managev1.ListArtistsAdminRequest{
		Pagination: &commonv1.PaginationRequest{Limit: query.Limit, Offset: query.Offset}, Filters: query.Filters, Sorts: query.Sorts,
	})
	return discovery.page, discovery.err
}
