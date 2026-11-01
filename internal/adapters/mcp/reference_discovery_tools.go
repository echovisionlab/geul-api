package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/artist"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

const (
	ToolReferenceSearch = "reference_search"
	ToolFileList        = "file_list"
)

const referenceSearchInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["reference_type","query"],
  "properties":{"reference_type":{"enum":["category","tag","client","map_place","member","artist"]},"query":{"type":"string","minLength":1},"limit":{"type":"integer","minimum":1,"maximum":100,"default":20,"description":"Maximum results. At most 50 for client and map_place; 100 for other types."}}
}`

const referenceSearchOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["reference_type","items","count","has_more"],
  "properties":{
    "reference_type":{"enum":["category","tag","client","map_place","member","artist"]},"count":{"type":"integer"},"has_more":{"type":["boolean","null"],"description":"True when more matches exist; false when the result is complete. Null means the quick-search service cap was reached and completeness is unknown; refine the query."},
    "items":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["id","name"],"properties":{"id":` + uuidJSONSchema + `,"name":{"type":"string"},"slug":{"type":"string"},"address":{"type":"string"},"status":{"type":"string"},"deleted":{"type":"boolean"}}}}
  }
}`

const fileListInputJSONSchema = `{
  "type":"object","additionalProperties":false,
  "properties":{"query":{"type":"string"},"folder_id":{"description":"Folder UUID from a file_list item with item_type=folder.","allOf":[` + uuidJSONSchema + `]},"mime_type_prefix":{"type":"string"},"page_size":{"type":"integer","minimum":1,"maximum":100,"default":20},"page_token":{"type":"string"}}
}`

const fileListOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["items","total"],
  "properties":{
    "items":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["item_type","id","name","created_at"],"properties":{"item_type":{"enum":["file","folder"]},"id":` + uuidJSONSchema + `,"name":{"type":"string"},"mime_type":{"type":"string"},"file_size":{"type":"integer"},"folder_id":{"type":"string"},"usage_count":{"type":"integer"},"created_at":{"type":"string","format":"date-time"},"folder_path":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["id","name"],"properties":{"id":` + uuidJSONSchema + `,"name":{"type":"string"}}}}}}},
    "total":{"type":"integer"},"next_page_token":{"type":"string"}
  }
}`

var referenceDiscoveryTools = toolRegistry[ReferenceDiscoveryTools]{
	defineTool(oauthTool(ToolReferenceSearch, "Search content references", "Search canonical Category, Tag, Client, Map Place, Member, or Artist IDs before using them in content management tools. Client and Map Place limits are at most 50; other types allow 100. For Client, Map Place, and Member, has_more is null at a full service cap, so refine the query to find omitted candidates.", referenceSearchInputJSONSchema, referenceSearchOutputJSONSchema, true, false), (*ReferenceDiscoveryTools).searchReferences),
	defineTool(oauthTool(ToolFileList, "List or search Files", "Browse a File Manager folder or search Files and folders. Use returned File IDs for featured images and other file relations.", fileListInputJSONSchema, fileListOutputJSONSchema, true, false), (*ReferenceDiscoveryTools).listFiles),
}

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
	SearchMembers(context.Context, member.SearchMembersQuery) ([]member.Summary, error)
}
type ArtistReferenceDiscovery interface {
	ListArtists(context.Context, artist.ListArtistsQuery) (artist.ArtistPage, error)
}
type FileReferenceDiscovery interface {
	ListFileManagerItems(context.Context, filemedia.ListFileManagerItemsQuery) (filemedia.FileManagerItemPage, error)
}

type ReferenceDiscoveryTools struct {
	categories CategoryReferenceDiscovery
	tags       TagReferenceDiscovery
	clients    ClientReferenceDiscovery
	mapPlaces  MapPlaceReferenceDiscovery
	members    MemberReferenceDiscovery
	artists    ArtistReferenceDiscovery
	files      FileReferenceDiscovery
}

func NewReferenceDiscoveryTools(categories CategoryReferenceDiscovery, tags TagReferenceDiscovery, clients ClientReferenceDiscovery, mapPlaces MapPlaceReferenceDiscovery, members MemberReferenceDiscovery, artists ArtistReferenceDiscovery, files FileReferenceDiscovery) (*ReferenceDiscoveryTools, error) {
	if interfaceValueIsNil(categories) || interfaceValueIsNil(tags) || interfaceValueIsNil(clients) || interfaceValueIsNil(mapPlaces) || interfaceValueIsNil(members) || interfaceValueIsNil(artists) || interfaceValueIsNil(files) {
		return nil, errors.New("MCP content reference discovery applications are required")
	}
	return &ReferenceDiscoveryTools{categories: categories, tags: tags, clients: clients, mapPlaces: mapPlaces, members: members, artists: artists, files: files}, nil
}

func (*ReferenceDiscoveryTools) ToolNames() []string { return referenceDiscoveryTools.names() }
func (*ReferenceDiscoveryTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return referenceDiscoveryTools.list(), nil
}

func (tools *ReferenceDiscoveryTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	return referenceDiscoveryTools.call(tools, ctx, name, arguments)
}

type referenceSearchArguments struct {
	ReferenceType string `json:"reference_type"`
	Query         string `json:"query"`
	Limit         int32  `json:"limit,omitempty"`
}

func (tools *ReferenceDiscoveryTools) searchReferences(ctx context.Context, _ string, call toolCall[referenceSearchArguments]) (mcpserver.ToolResult, error) {
	var input referenceSearchArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	input.Query = strings.TrimSpace(input.Query)
	if input.Query == "" {
		return executionError(errors.New("query is required"))
	}
	if input.Limit == 0 {
		input.Limit = 20
	}
	maximum := int32(100)
	if input.ReferenceType == "client" || input.ReferenceType == "map_place" {
		maximum = 50
	}
	if input.Limit < 1 || input.Limit > maximum {
		return executionError(fmt.Errorf("limit must be between 1 and %d for %s references", maximum, input.ReferenceType))
	}
	items := make([]map[string]any, 0)
	var hasMore any
	searchFilter := discoverySearchFilters(input.Query)
	pagination := &commonv1.PaginationRequest{Limit: input.Limit}
	switch input.ReferenceType {
	case "category":
		page, err := tools.categories.ListCategories(ctx, referencecatalog.ReferenceListQuery{Filters: searchFilter, Limit: input.Limit})
		if err != nil {
			return expectedToolError(err)
		}
		hasMore = page.Pagination.HasMore
		for _, item := range page.Items {
			items = append(items, referenceItem(item.ID, item.Name, &item.Slug, "", "", false))
		}
	case "tag":
		page, err := tools.tags.ListTags(ctx, referencecatalog.ReferenceListQuery{Filters: searchFilter, Limit: input.Limit})
		if err != nil {
			return expectedToolError(err)
		}
		hasMore = page.Pagination.HasMore
		for _, item := range page.Items {
			items = append(items, referenceItem(item.ID, item.Name, &item.Slug, "", "", false))
		}
	case "client":
		clients, err := tools.clients.SearchClients(ctx, referencecatalog.SearchClientsQuery{Query: input.Query, Limit: min(input.Limit+1, maximum)})
		if err != nil {
			return expectedToolError(err)
		}
		hasMore = quickSearchHasMore(len(clients), input.Limit, maximum)
		for _, item := range clients[:min(len(clients), int(input.Limit))] {
			items = append(items, referenceItem(item.Client.ID, item.Client.Name, nil, "", "", false))
		}
	case "map_place":
		places, err := tools.mapPlaces.SearchMapPlaces(ctx, referencecatalog.SearchMapPlacesQuery{Query: input.Query, Limit: min(input.Limit+1, maximum)})
		if err != nil {
			return expectedToolError(err)
		}
		hasMore = quickSearchHasMore(len(places), input.Limit, maximum)
		for _, item := range places[:min(len(places), int(input.Limit))] {
			items = append(items, referenceItem(item.ID, item.Name, nil, item.Address, "", false))
		}
	case "member":
		members, err := tools.members.SearchMembers(ctx, member.SearchMembersQuery{Query: input.Query, Limit: min(input.Limit+1, maximum)})
		if err != nil {
			return expectedToolError(err)
		}
		hasMore = quickSearchHasMore(len(members), input.Limit, maximum)
		for _, item := range members[:min(len(members), int(input.Limit))] {
			items = append(items, referenceItem(item.ID, item.Nickname, nil, "", "", item.Deleted))
		}
	case "artist":
		page, err := tools.artists.ListArtists(ctx, artist.ListArtistsQuery{Limit: pagination.GetLimit(), Offset: pagination.GetOffset(), Filters: searchFilter})
		if err != nil {
			return expectedToolError(err)
		}
		hasMore = page.Pagination.HasMore
		for _, item := range page.Artists {
			items = append(items, referenceItem(item.Artist.ID, item.Name, item.Artist.Slug, "", string(item.Artist.Status), false))
		}
	default:
		return executionError(fmt.Errorf("unsupported reference_type %q", input.ReferenceType))
	}
	output := map[string]any{"reference_type": input.ReferenceType, "items": items, "count": len(items), "has_more": hasMore}
	// contentResult omits nil optional fields; completeness is required even when unknown.
	encoded, err := json.Marshal(output)
	if err != nil {
		return mcpserver.ToolResult{}, fmt.Errorf("encode reference search: %w", err)
	}
	return structuredResult(encoded, false)
}

func quickSearchHasMore(count int, limit, maximum int32) any {
	if count > int(limit) {
		return true
	}
	if count == int(maximum) {
		return nil
	}
	return false
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

type fileListArguments struct {
	Query          *string `json:"query,omitempty"`
	FolderID       *string `json:"folder_id,omitempty"`
	MIMETypePrefix *string `json:"mime_type_prefix,omitempty"`
	PageSize       int32   `json:"page_size,omitempty"`
	PageToken      *string `json:"page_token,omitempty"`
}

func (tools *ReferenceDiscoveryTools) listFiles(ctx context.Context, _ string, call toolCall[fileListArguments]) (mcpserver.ToolResult, error) {
	arguments := call.Arguments

	if err := rejectNullArguments(arguments, "page_size"); err != nil {
		return executionError(err)
	}
	var input fileListArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if _, supplied := arguments["page_size"]; !supplied {
		input.PageSize = 20
	}
	if input.PageSize < 1 || input.PageSize > 100 {
		return executionError(errors.New("page_size must be between 1 and 100, or omitted for the default"))
	}
	response, err := tools.files.ListFileManagerItems(ctx, filemedia.ListFileManagerItemsQuery{
		FolderID: optionalString(input.FolderID), Query: optionalString(input.Query), MimeTypePrefix: optionalString(input.MIMETypePrefix),
		SortField: filemedia.LibrarySortUpdatedAt,
		SortOrder: filemedia.SortDesc, PageSize: input.PageSize, PageToken: optionalString(input.PageToken),
	})
	if err != nil {
		return expectedToolError(err)
	}
	items := make([]map[string]any, 0, len(response.Items))
	for _, item := range response.Items {
		if item == nil {
			continue
		}
		path := make([]map[string]any, 0, len(item.FolderPath))
		for _, segment := range item.FolderPath {
			if segment != nil {
				path = append(path, map[string]any{"id": segment.ID, "name": segment.Name})
			}
		}
		if file := item.File; file != nil {
			output := map[string]any{"item_type": "file", "id": file.ID, "name": file.FileName, "mime_type": file.MimeType, "file_size": file.FileSize, "usage_count": file.UsageCount, "created_at": fileManagerTimeString(file.CreatedAt), "folder_path": path}
			if file.FolderID != nil {
				output["folder_id"] = *file.FolderID
			}
			items = append(items, output)
			continue
		}
		if folder := item.Folder; folder != nil {
			output := map[string]any{"item_type": "folder", "id": folder.ID, "name": folder.Name, "created_at": fileManagerTimeString(folder.CreatedAt), "folder_path": path}
			if folder.ParentID != nil {
				output["folder_id"] = *folder.ParentID
			}
			items = append(items, output)
		}
	}
	return contentResult(map[string]any{"items": items, "total": response.Total, "next_page_token": optionalStringValue(response.NextPageToken)})
}

var _ ToolProvider = (*ReferenceDiscoveryTools)(nil)
