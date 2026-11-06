package mcp

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/echovisionlab/geul-api/internal/artist"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	memberdomain "github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/memberref"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
)

func TestReferenceSearchBuildsExactCategoryRequest(t *testing.T) {
	categories := &recordingCategoryReferences{}
	tools := referenceSearchOver(t, ReferenceSearchApplications{Categories: categories})
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolReferenceSearch, toolArguments(t, `{"reference_type":"category","query":"Sound","limit":12}`))
	if err != nil {
		t.Fatal(err)
	}
	if categories.query.Limit != 12 || len(categories.query.Filters) != 1 || categories.query.Filters[0].Field != "search" || categories.query.Filters[0].Value != "Sound" {
		t.Fatalf("category query = %#v", categories.query)
	}
	items := result.StructuredContent["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("category result = %#v", result.StructuredContent)
	}
}

func TestReferenceQuickSearchProbesTruncationWithinServiceCaps(t *testing.T) {
	for _, referenceType := range []string{"client", "map_place", "member"} {
		maximum := 50
		if referenceType == "member" {
			maximum = 100
		}
		for _, test := range []struct {
			name                                 string
			limit, count, wantRequest, wantCount int
			hasMore                              any
		}{
			{"complete", 20, 5, 21, 5, false},
			{"extra match", 20, 21, 21, 20, true},
			{"full cap is unknown", maximum, maximum, maximum, maximum, nil},
			{"short cap is complete", maximum, maximum - 1, maximum, maximum - 1, false},
		} {
			t.Run(referenceType+"/"+test.name, func(t *testing.T) {
				clients, places, members := &recordingClientReferences{count: test.count}, &recordingMapPlaceReferences{count: test.count}, &recordingMemberReferences{count: test.count}
				tools := referenceSearchOver(t, ReferenceSearchApplications{Clients: clients, MapPlaces: places, Members: members})
				result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolReferenceSearch, toolArguments(t, fmt.Sprintf(`{"reference_type":%q,"query":"Candidate","limit":%d}`, referenceType, test.limit)))
				if err != nil {
					t.Fatal(err)
				}
				requestLimit := int32(0)
				switch referenceType {
				case "client":
					requestLimit = clients.query.Limit
				case "map_place":
					requestLimit = places.query.Limit
				case "member":
					requestLimit = members.query.Limit
				}
				if requestLimit != int32(test.wantRequest) {
					t.Fatalf("service limit=%d want=%d", requestLimit, test.wantRequest)
				}
				hasMore, present := result.StructuredContent["has_more"]
				if !present || hasMore != test.hasMore || result.StructuredContent["count"] != float64(test.wantCount) || len(result.StructuredContent["items"].([]any)) != test.wantCount {
					t.Fatalf("result=%#v", result.StructuredContent)
				}
			})
		}
	}
}

func TestReferenceQuickSearchRejectsLimitOutsideServiceCap(t *testing.T) {
	tools := referenceSearchOver(t, ReferenceSearchApplications{})
	for _, test := range []struct {
		referenceType string
		limit         int
	}{{"client", 51}, {"map_place", 100}, {"member", 101}} {
		_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolReferenceSearch, toolArguments(t, fmt.Sprintf(`{"reference_type":%q,"query":"Candidate","limit":%d}`, test.referenceType, test.limit)))
		var executionErr *mcpserver.ToolExecutionError
		if !errors.As(err, &executionErr) {
			t.Fatalf("%s limit %d was accepted: %v", test.referenceType, test.limit, err)
		}
	}
}

type recordingCategoryReferences struct {
	query *referencecatalog.ReferenceListQuery
}

func (r *recordingCategoryReferences) ListCategories(_ context.Context, query referencecatalog.ReferenceListQuery) (referencecatalog.Page[referencecatalog.Reference], error) {
	r.query = &query
	return referencecatalog.Page[referencecatalog.Reference]{
		Items:      []referencecatalog.Reference{{ID: "11111111-1111-4111-8111-111111111111", Name: "Sound", Slug: "sound"}},
		Pagination: referencecatalog.Pagination{Total: 1, Limit: query.Limit},
	}, nil
}

type recordingTagReferences struct{}

func (recordingTagReferences) ListTags(context.Context, referencecatalog.ReferenceListQuery) (referencecatalog.Page[referencecatalog.Reference], error) {
	return referencecatalog.Page[referencecatalog.Reference]{}, errors.New("unexpected tag search")
}

type recordingClientReferences struct {
	query *referencecatalog.SearchClientsQuery
	count int
}

func (r *recordingClientReferences) SearchClients(_ context.Context, query referencecatalog.SearchClientsQuery) ([]referencecatalog.ClientView, error) {
	r.query = &query
	items := make([]referencecatalog.ClientView, r.count)
	for index := range items {
		items[index] = referencecatalog.ClientView{Client: referencecatalog.Client{ID: managementWorkID, Name: "Client"}}
	}
	return items, nil
}

type recordingMapPlaceReferences struct {
	query *referencecatalog.SearchMapPlacesQuery
	count int
}

func (r *recordingMapPlaceReferences) SearchMapPlaces(_ context.Context, query referencecatalog.SearchMapPlacesQuery) ([]referencecatalog.MapPlace, error) {
	r.query = &query
	items := make([]referencecatalog.MapPlace, r.count)
	for index := range items {
		items[index] = referencecatalog.MapPlace{ID: managementWorkID, Name: "Place"}
	}
	return items, nil
}

type recordingMemberReferences struct {
	query memberdomain.SearchMembersQuery
	count int
}

func (r *recordingMemberReferences) SearchMembers(_ context.Context, query memberdomain.SearchMembersQuery) ([]memberref.Summary, error) {
	r.query = query
	items := make([]memberref.Summary, r.count)
	for index := range items {
		items[index] = memberref.Summary{ID: managementWorkID, Nickname: "Member"}
	}
	return items, nil
}

type recordingArtistReferences struct{}

func (*recordingArtistReferences) ListArtists(context.Context, artist.ListArtistsQuery) (artist.ArtistPage, error) {
	return artist.ArtistPage{}, nil
}
