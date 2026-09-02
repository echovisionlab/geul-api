package mcp

import (
	"context"
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
  "properties":{"reference_type":{"enum":["category","tag","client","map_place","member"]},"query":{"type":"string","minLength":1},"limit":{"type":"integer","minimum":1,"maximum":100,"default":20}}
}`

const referenceSearchOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["reference_type","items","count","has_more"],
  "properties":{
    "reference_type":{"enum":["category","tag","client","map_place","member"]},"count":{"type":"integer"},"has_more":{"type":"boolean"},
    "items":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["id","name"],"properties":{"id":` + documentReferenceJSONSchema + `,"name":{"type":"string"},"slug":{"type":"string"},"address":{"type":"string"},"status":{"type":"string"},"deleted":{"type":"boolean"}}}}
  }
}`

const fileListInputJSONSchema = `{
  "type":"object","additionalProperties":false,
  "properties":{"query":{"type":"string"},"folder_id":` + documentReferenceJSONSchema + `,"mime_type_prefix":{"type":"string"},"page_size":{"type":"integer","minimum":1,"maximum":100,"default":20},"page_token":{"type":"string"}}
}`

const fileListOutputJSONSchema = `{
  "type":"object","additionalProperties":false,"required":["items","total"],
  "properties":{
    "items":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["item_type","id","name","created_at"],"properties":{"item_type":{"enum":["file","folder"]},"id":` + documentReferenceJSONSchema + `,"name":{"type":"string"},"mime_type":{"type":"string"},"file_size":{"type":"integer"},"folder_id":{"type":"string"},"usage_count":{"type":"integer"},"created_at":{"type":"string","format":"date-time"},"folder_path":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["id","name"],"properties":{"id":` + documentReferenceJSONSchema + `,"name":{"type":"string"}}}}}}},
    "total":{"type":"integer"},"next_page_token":{"type":"string"}
  }
}`

var referenceDiscoveryTools = []mcpserver.Tool{
	relatedTool(ToolReferenceSearch, "Search content references", "Search canonical Category, Tag, Client, Map Place, or Member IDs before using them in content management tools.", referenceSearchInputJSONSchema, referenceSearchOutputJSONSchema, true, false),
	relatedTool(ToolFileList, "List or search Files", "Browse a File Manager folder or search Geul Files and folders. Use returned File IDs for featured images and other file relations.", fileListInputJSONSchema, fileListOutputJSONSchema, true, false),
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
type FileReferenceDiscovery interface {
	ListFileManagerItems(context.Context, *connect.Request[managev1.ListFileManagerItemsRequest]) (*connect.Response[managev1.ListFileManagerItemsResponse], error)
}

type ReferenceDiscoveryTools struct {
	categories CategoryReferenceDiscovery
	tags       TagReferenceDiscovery
	clients    ClientReferenceDiscovery
	mapPlaces  MapPlaceReferenceDiscovery
	members    MemberReferenceDiscovery
	files      FileReferenceDiscovery
}

func NewReferenceDiscoveryTools(categories CategoryReferenceDiscovery, tags TagReferenceDiscovery, clients ClientReferenceDiscovery, mapPlaces MapPlaceReferenceDiscovery, members MemberReferenceDiscovery, files FileReferenceDiscovery) (*ReferenceDiscoveryTools, error) {
	if interfaceValueIsNil(categories) || interfaceValueIsNil(tags) || interfaceValueIsNil(clients) || interfaceValueIsNil(mapPlaces) || interfaceValueIsNil(members) || interfaceValueIsNil(files) {
		return nil, errors.New("MCP content reference discovery applications are required")
	}
	return &ReferenceDiscoveryTools{categories: categories, tags: tags, clients: clients, mapPlaces: mapPlaces, members: members, files: files}, nil
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
	items := make([]map[string]any, 0)
	hasMore := false
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
		response, err := tools.clients.SearchClients(ctx, connect.NewRequest(&managev1.SearchClientsRequest{Query: input.Query, Limit: input.Limit}))
		if err != nil {
			return expectedToolError(err)
		}
		for _, item := range response.Msg.Clients {
			if item != nil {
				items = append(items, referenceItem(item.Id, item.Name, nil, "", "", false))
			}
		}
	case "map_place":
		response, err := tools.mapPlaces.SearchMapPlaces(ctx, connect.NewRequest(&managev1.SearchMapPlacesRequest{Query: input.Query, Limit: input.Limit}))
		if err != nil {
			return expectedToolError(err)
		}
		for _, item := range response.Msg.Places {
			if item != nil {
				items = append(items, referenceItem(item.Id, item.Name, nil, item.Address, "", false))
			}
		}
	case "member":
		response, err := tools.members.SearchMembers(ctx, connect.NewRequest(&managev1.SearchMembersRequest{Query: input.Query, Limit: input.Limit}))
		if err != nil {
			return expectedToolError(err)
		}
		for _, item := range response.Msg.Members {
			if item != nil {
				items = append(items, referenceItem(item.Id, item.Nickname, nil, "", "", item.Deleted))
			}
		}
	default:
		return executionError(fmt.Errorf("unsupported reference_type %q", input.ReferenceType))
	}
	return contentResult(map[string]any{"reference_type": input.ReferenceType, "items": items, "count": len(items), "has_more": hasMore})
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
	var input fileListArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	if input.PageSize == 0 {
		input.PageSize = 20
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
