package mcp

import (
	"context"
	"strings"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/artist"
	"github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/memberref"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
)

type CategoryReferenceDiscovery interface {
	ListCategories(context.Context, referencecatalog.ReferenceListQuery) (referencecatalog.Page[referencecatalog.Reference], error)
}

type TagReferenceDiscovery interface {
	ListTags(context.Context, referencecatalog.ReferenceListQuery) (referencecatalog.Page[referencecatalog.Reference], error)
}

type ClientReferenceDiscovery interface {
	SearchClients(context.Context, referencecatalog.SearchClientsQuery) ([]referencecatalog.ClientView, error)
}

type MapPlaceReferenceDiscovery interface {
	SearchMapPlaces(context.Context, referencecatalog.SearchMapPlacesQuery) ([]referencecatalog.MapPlace, error)
}

type MemberReferenceDiscovery interface {
	SearchMembers(context.Context, member.SearchMembersQuery) ([]memberref.Summary, error)
}

type ArtistReferenceDiscovery interface {
	ListArtists(context.Context, artist.ListArtistsQuery) (artist.ArtistPage, error)
}

// quickSearchRequest asks a quick search for the names matching a query.
type quickSearchRequest struct {
	Query   string
	Limit   int32
	Maximum int32
}

// quickReferenceSearch is a reference type found by name. It refines by query and has
// no offset.
type quickReferenceSearch struct {
	maximum int32
	read    func(context.Context, quickSearchRequest) (referenceSearchOutput, error)
}

// quickSearchServiceCap is the most a client or map place search returns.
const quickSearchServiceCap int32 = 50

func newQuickReferenceSearches(applications ReferenceSearchApplications) map[string]quickReferenceSearch {
	return map[string]quickReferenceSearch{
		"category": {maximum: referenceSearchMaximumLimit, read: func(ctx context.Context, request quickSearchRequest) (referenceSearchOutput, error) {
			page, err := applications.Categories.ListCategories(ctx, referencecatalog.ReferenceListQuery{Filters: discoverySearchFilters(request.Query), Limit: request.Limit})
			return catalogReferences(page, err)
		}},
		"tag": {maximum: referenceSearchMaximumLimit, read: func(ctx context.Context, request quickSearchRequest) (referenceSearchOutput, error) {
			page, err := applications.Tags.ListTags(ctx, referencecatalog.ReferenceListQuery{Filters: discoverySearchFilters(request.Query), Limit: request.Limit})
			return catalogReferences(page, err)
		}},
		"client": {maximum: quickSearchServiceCap, read: func(ctx context.Context, request quickSearchRequest) (referenceSearchOutput, error) {
			clients, err := applications.Clients.SearchClients(ctx, referencecatalog.SearchClientsQuery{Query: request.Query, Limit: request.lookahead()})
			return cappedReferences(clients, err, request, func(item referencecatalog.ClientView) map[string]any {
				return referenceItem(item.Client.ID, item.Client.Name, nil, "", "", false)
			})
		}},
		"map_place": {maximum: quickSearchServiceCap, read: func(ctx context.Context, request quickSearchRequest) (referenceSearchOutput, error) {
			places, err := applications.MapPlaces.SearchMapPlaces(ctx, referencecatalog.SearchMapPlacesQuery{Query: request.Query, Limit: request.lookahead()})
			return cappedReferences(places, err, request, func(item referencecatalog.MapPlace) map[string]any {
				return referenceItem(item.ID, item.Name, nil, item.Address, "", false)
			})
		}},
		"member": {maximum: referenceSearchMaximumLimit, read: func(ctx context.Context, request quickSearchRequest) (referenceSearchOutput, error) {
			members, err := applications.Members.SearchMembers(ctx, member.SearchMembersQuery{Query: request.Query, Limit: request.lookahead()})
			return cappedReferences(members, err, request, func(item memberref.Summary) map[string]any {
				return referenceItem(item.ID, item.Nickname, nil, "", "", item.Deleted)
			})
		}},
		"artist": {maximum: referenceSearchMaximumLimit, read: func(ctx context.Context, request quickSearchRequest) (referenceSearchOutput, error) {
			page, err := applications.Artists.ListArtists(ctx, artist.ListArtistsQuery{Limit: request.Limit, Filters: discoverySearchFilters(request.Query)})
			if err != nil {
				return referenceSearchOutput{}, err
			}
			items := make([]map[string]any, 0, len(page.Artists))
			for _, item := range page.Artists {
				items = append(items, referenceItem(item.Artist.ID, item.Name, item.Artist.Slug, "", string(item.Artist.Status), false))
			}
			return referenceSearchOutput{Items: items, HasMore: &page.Pagination.HasMore}, nil
		}},
	}
}

// run checks input against this search and reads it.
func (search quickReferenceSearch) run(ctx context.Context, input referenceSearchArguments) (referenceSearchOutput, error) {
	if input.Offset != nil || input.Status != nil {
		return referenceSearchOutput{}, &core.InputError{Message: input.ReferenceType + " takes query and limit; refine the query to see more"}
	}
	query := strings.TrimSpace(input.query())
	if query == "" {
		return referenceSearchOutput{}, &core.InputError{Message: "query is required"}
	}
	limit, err := input.limit(search.maximum)
	if err != nil {
		return referenceSearchOutput{}, &core.InputError{Message: err.Error()}
	}
	return search.read(ctx, quickSearchRequest{Query: query, Limit: limit, Maximum: search.maximum})
}

// lookahead asks for one more than the limit, so that an extra row shows more exist.
func (request quickSearchRequest) lookahead() int32 {
	return min(request.Limit+1, request.Maximum)
}

func catalogReferences(page referencecatalog.Page[referencecatalog.Reference], err error) (referenceSearchOutput, error) {
	if err != nil {
		return referenceSearchOutput{}, err
	}
	items := make([]map[string]any, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, referenceItem(item.ID, item.Name, &item.Slug, "", "", false))
	}
	return referenceSearchOutput{Items: items, HasMore: &page.Pagination.HasMore}, nil
}

// cappedReferences reads a search that asked for its lookahead. A search cut off by
// its service cap cannot tell whether more exist.
func cappedReferences[Row any](rows []Row, err error, request quickSearchRequest, project func(Row) map[string]any) (referenceSearchOutput, error) {
	if err != nil {
		return referenceSearchOutput{}, err
	}
	shown := rows[:min(len(rows), int(request.Limit))]
	items := make([]map[string]any, 0, len(shown))
	for _, row := range shown {
		items = append(items, project(row))
	}
	output := referenceSearchOutput{Items: items}
	if hasMore := len(rows) > int(request.Limit); hasMore || len(rows) != int(request.Maximum) {
		output.HasMore = &hasMore
	}
	return output, nil
}

func referenceItem(id, name string, slug *string, address, status string, deleted bool) map[string]any {
	item := map[string]any{"id": id, "name": name}
	if slug != nil {
		item["slug"] = *slug
	}
	if address != "" {
		item["address"] = address
	}
	if status != "" {
		item["status"] = status
	}
	if deleted {
		item["deleted"] = true
	}
	return item
}
