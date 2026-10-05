package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const (
	ToolReferenceSearch = "reference_search"
	ToolFileList        = "file_list"
)

const referenceSearchInputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["reference_type","query"],
  "properties":{"reference_type":{"enum":["category","tag","client","map_place","member","artist"]},"query":{"type":"string","minLength":1},"limit":{"type":"integer","minimum":1,"maximum":100,"default":20}},
  "allOf":[{"if":{"properties":{"reference_type":{"enum":["client","map_place"]}},"required":["reference_type"]},"then":{"properties":{"limit":{"maximum":50}}}}]
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

var referenceDiscoveryTools = []mcpserver.Tool{
	oauthTool(ToolReferenceSearch, "Search content references", "Search canonical Category, Tag, Client, Map Place, Member, or Artist IDs before using them in content management tools. Client and Map Place limits are at most 50; other types allow 100. For Client, Map Place, and Member, has_more is null at a full service cap, so refine the query to find omitted candidates.", referenceSearchInputJSONSchema, referenceSearchOutputJSONSchema, true, false),
	oauthTool(ToolFileList, "List or search Files", "Browse a File Manager folder or search Files and folders. Use returned File IDs for featured images and other file relations.", fileListInputJSONSchema, fileListOutputJSONSchema, true, false),
}

type CategoryReferenceDiscovery interface {
	ListCategories(context.Context, *connect.Request[managev1.ListCategoriesRequest]) (*connect.Response[managev1.ListCategoriesResponse], error)
}
type TagReferenceDiscovery interface {
	ListTags(context.Context, *connect.Request[managev1.ListTagsRequest]) (*connect.Response[managev1.ListTagsResponse], error)
}
type ClientReferenceDiscovery interface {
	SearchClients(context.Context, *connect.Request[managev1.SearchClientsRequest]) (*connect.Response[managev1.SearchClientsResponse], error)
}
type MapPlaceReferenceDiscovery interface {
	SearchMapPlaces(context.Context, *connect.Request[managev1.SearchMapPlacesRequest]) (*connect.Response[managev1.SearchMapPlacesResponse], error)
}
type MemberReferenceDiscovery interface {
	SearchMembers(context.Context, *connect.Request[managev1.SearchMembersRequest]) (*connect.Response[managev1.SearchMembersResponse], error)
}
type ArtistReferenceDiscovery interface {
	ListArtists(context.Context, *connect.Request[managev1.ListArtistsRequest]) (*connect.Response[managev1.ListArtistsResponse], error)
}
type FileReferenceDiscovery interface {
	ListFileManagerItems(context.Context, *connect.Request[managev1.ListFileManagerItemsRequest]) (*connect.Response[managev1.ListFileManagerItemsResponse], error)
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

func (*ReferenceDiscoveryTools) ToolNames() []string {
	return toolDefinitionNames(referenceDiscoveryTools)
}
func (*ReferenceDiscoveryTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return cloneToolDefinitions(referenceDiscoveryTools), nil
}

func (tools *ReferenceDiscoveryTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	switch name {
	case ToolReferenceSearch:
		return tools.searchReferences(ctx, arguments)
	case ToolFileList:
		return tools.listFiles(ctx, arguments)
	default:
		return mcpserver.ToolResult{}, mcpserver.ErrUnknownTool
	}
}

type referenceSearchArguments struct {
	ReferenceType string `json:"reference_type"`
	Query         string `json:"query"`
	Limit         int32  `json:"limit,omitempty"`
}

func (tools *ReferenceDiscoveryTools) searchReferences(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input referenceSearchArguments
	if err := decodeArguments(arguments, &input); err != nil {
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
	var hasMore any = false
	searchFilter := discoverySearchFilters(input.Query)
	pagination := &commonv1.PaginationRequest{Limit: input.Limit}
	switch input.ReferenceType {
	case "category":
		response, err := tools.categories.ListCategories(ctx, connect.NewRequest(&managev1.ListCategoriesRequest{Pagination: pagination, Filters: searchFilter}))
		if err != nil {
			return expectedToolError(err)
		}
		hasMore = response.Msg.Pagination.GetHasMore()
		for _, item := range response.Msg.Categories {
			if item != nil {
				items = append(items, referenceItem(item.Id, item.Name, item.Slug, "", "", false))
			}
		}
	case "tag":
		response, err := tools.tags.ListTags(ctx, connect.NewRequest(&managev1.ListTagsRequest{Pagination: pagination, Filters: searchFilter}))
		if err != nil {
			return expectedToolError(err)
		}
		hasMore = response.Msg.Pagination.GetHasMore()
		for _, item := range response.Msg.Tags {
			if item != nil {
				items = append(items, referenceItem(item.Id, item.Name, item.Slug, "", "", false))
			}
		}
	case "client":
		response, err := tools.clients.SearchClients(ctx, connect.NewRequest(&managev1.SearchClientsRequest{Query: input.Query, Limit: min(input.Limit+1, maximum)}))
		if err != nil {
			return expectedToolError(err)
		}
		hasMore = quickSearchHasMore(len(response.Msg.Clients), input.Limit, maximum)
		for _, item := range response.Msg.Clients[:min(len(response.Msg.Clients), int(input.Limit))] {
			if item != nil {
				items = append(items, referenceItem(item.Id, item.Name, nil, "", "", false))
			}
		}
	case "map_place":
		response, err := tools.mapPlaces.SearchMapPlaces(ctx, connect.NewRequest(&managev1.SearchMapPlacesRequest{Query: input.Query, Limit: min(input.Limit+1, maximum)}))
		if err != nil {
			return expectedToolError(err)
		}
		hasMore = quickSearchHasMore(len(response.Msg.Places), input.Limit, maximum)
		for _, item := range response.Msg.Places[:min(len(response.Msg.Places), int(input.Limit))] {
			if item != nil {
				items = append(items, referenceItem(item.Id, item.Name, nil, item.Address, "", false))
			}
		}
	case "member":
		response, err := tools.members.SearchMembers(ctx, connect.NewRequest(&managev1.SearchMembersRequest{Query: input.Query, Limit: min(input.Limit+1, maximum)}))
		if err != nil {
			return expectedToolError(err)
		}
		hasMore = quickSearchHasMore(len(response.Msg.Members), input.Limit, maximum)
		for _, item := range response.Msg.Members[:min(len(response.Msg.Members), int(input.Limit))] {
			if item != nil {
				items = append(items, referenceItem(item.Id, item.Nickname, nil, "", "", item.Deleted))
			}
		}
	case "artist":
		response, err := tools.artists.ListArtists(ctx, connect.NewRequest(&managev1.ListArtistsRequest{Pagination: pagination, Filters: searchFilter}))
		if err != nil {
			return expectedToolError(err)
		}
		hasMore = response.Msg.Pagination.GetHasMore()
		for _, item := range response.Msg.Artists {
			if item != nil {
				items = append(items, referenceItem(item.Id, item.Name, item.Slug, "", item.Status, false))
			}
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

func (tools *ReferenceDiscoveryTools) listFiles(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	if err := rejectNullArguments(arguments, "page_size"); err != nil {
		return executionError(err)
	}
	var input fileListArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	if _, supplied := arguments["page_size"]; !supplied {
		input.PageSize = 20
	}
	if input.PageSize < 1 || input.PageSize > 100 {
		return executionError(errors.New("page_size must be between 1 and 100, or omitted for the default"))
	}
	response, err := tools.files.ListFileManagerItems(ctx, connect.NewRequest(&managev1.ListFileManagerItemsRequest{
		FolderId: input.FolderID, Query: input.Query, MimeTypePrefix: input.MIMETypePrefix,
		SortField: managev1.FileManagerSortField_FILE_MANAGER_SORT_FIELD_UPDATED_AT,
		SortOrder: commonv1.SortOrder_SORT_ORDER_DESC, PageSize: input.PageSize, PageToken: input.PageToken,
	}))
	if err != nil {
		return expectedToolError(err)
	}
	items := make([]map[string]any, 0, len(response.Msg.Items))
	for _, item := range response.Msg.Items {
		if item == nil {
			continue
		}
		path := make([]map[string]any, 0, len(item.FolderPath))
		for _, segment := range item.FolderPath {
			if segment != nil {
				path = append(path, map[string]any{"id": segment.Id, "name": segment.Name})
			}
		}
		if file := item.GetFile(); file != nil {
			output := map[string]any{"item_type": "file", "id": file.Id, "name": file.FileName, "mime_type": file.MimeType, "file_size": file.FileSize, "usage_count": file.UsageCount, "created_at": timestampString(file.CreatedAt), "folder_path": path}
			if file.FolderId != nil {
				output["folder_id"] = *file.FolderId
			}
			items = append(items, output)
			continue
		}
		if folder := item.GetFolder(); folder != nil {
			output := map[string]any{"item_type": "folder", "id": folder.Id, "name": folder.Name, "created_at": timestampString(folder.CreatedAt), "folder_path": path}
			if folder.ParentId != nil {
				output["folder_id"] = *folder.ParentId
			}
			items = append(items, output)
		}
	}
	return contentResult(map[string]any{"items": items, "total": response.Msg.Total, "next_page_token": optionalStringValue(response.Msg.NextPageToken)})
}

var _ ToolProvider = (*ReferenceDiscoveryTools)(nil)
