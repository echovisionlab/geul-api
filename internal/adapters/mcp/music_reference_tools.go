package mcp

import (
	"context"
	"errors"

	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const (
	ToolGenreList  = "genre_list"
	ToolStyleList  = "style_list"
	ToolFormatList = "format_list"
)

var musicReferenceTools = []mcpserver.Tool{
	oauthTool(ToolGenreList, "List Genres", "List or search canonical Genre IDs and native catalog names before selecting Release genre relations. Continue with next_offset and the same query. Authorization remains in the native admin list.", musicReferenceListInputJSONSchema, musicNamedReferenceListOutputJSONSchema, true, false),
	oauthTool(ToolStyleList, "List Styles", "List or search canonical Style IDs and native catalog names before selecting Release style relations. Continue with next_offset and the same query. Authorization remains in the native admin list.", musicReferenceListInputJSONSchema, musicNamedReferenceListOutputJSONSchema, true, false),
	oauthTool(ToolFormatList, "List Formats", "List or search canonical Format IDs and native catalog names before selecting Release format relations. Continue with next_offset and the same query. Authorization remains in the native admin list.", musicReferenceListInputJSONSchema, musicFormatListOutputJSONSchema, true, false),
}

type GenreReferenceDiscovery interface {
	ListGenresAdmin(context.Context, referencecatalog.ReferenceListQuery) (referencecatalog.Page[*managev1.GenreWithStats], error)
}

type StyleReferenceDiscovery interface {
	ListStylesAdmin(context.Context, referencecatalog.ReferenceListQuery) (referencecatalog.Page[*managev1.StyleWithStats], error)
}

type FormatReferenceDiscovery interface {
	ListFormatsAdmin(context.Context, referencecatalog.ReferenceListQuery) (referencecatalog.Page[*managev1.FormatWithStats], error)
}

type MusicReferenceTools struct {
	genres  GenreReferenceDiscovery
	styles  StyleReferenceDiscovery
	formats FormatReferenceDiscovery
}

func NewMusicReferenceTools(genres GenreReferenceDiscovery, styles StyleReferenceDiscovery, formats FormatReferenceDiscovery) (*MusicReferenceTools, error) {
	if interfaceValueIsNil(genres) || interfaceValueIsNil(styles) || interfaceValueIsNil(formats) {
		return nil, errors.New("MCP music reference discovery applications are required")
	}
	return &MusicReferenceTools{genres: genres, styles: styles, formats: formats}, nil
}

func (*MusicReferenceTools) ToolNames() []string {
	return toolDefinitionNames(musicReferenceTools)
}

func (*MusicReferenceTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return cloneToolDefinitions(musicReferenceTools), nil
}

func (tools *MusicReferenceTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	if name != ToolGenreList && name != ToolStyleList && name != ToolFormatList {
		return mcpserver.ToolResult{}, mcpserver.ErrUnknownTool
	}
	if err := rejectNullArguments(arguments, "query", "limit", "offset"); err != nil {
		return executionError(err)
	}
	var input struct {
		Query  string `json:"query,omitempty"`
		Limit  *int32 `json:"limit,omitempty"`
		Offset int32  `json:"offset,omitempty"`
	}
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	limit := int32(20)
	if input.Limit != nil {
		limit = *input.Limit
	}
	if limit < 1 || limit > 100 {
		return executionError(errors.New("limit must be between 1 and 100"))
	}
	if input.Offset < 0 {
		return executionError(errors.New("offset must be between 0 and 2147483647"))
	}
	query := referencecatalog.ReferenceListQuery{Filters: discoverySearchFilters(input.Query), Limit: limit, Offset: input.Offset}
	items := make([]map[string]any, 0)
	var responsePagination *commonv1.PaginationResponse
	switch name {
	case ToolGenreList:
		page, err := tools.genres.ListGenresAdmin(ctx, query)
		if err != nil {
			return expectedToolError(err)
		}
		responsePagination = page.Pagination
		for _, item := range page.Items {
			if item != nil && item.Genre != nil {
				genre := item.Genre
				projected := map[string]any{"id": genre.Id, "name": genre.Name, "slug": genre.Slug}
				if genre.Description != nil {
					projected["description"] = *genre.Description
				}
				items = append(items, projected)
			}
		}
	case ToolStyleList:
		page, err := tools.styles.ListStylesAdmin(ctx, query)
		if err != nil {
			return expectedToolError(err)
		}
		responsePagination = page.Pagination
		for _, item := range page.Items {
			if item != nil && item.Style != nil {
				style := item.Style
				projected := map[string]any{"id": style.Id, "name": style.Name, "slug": style.Slug}
				if style.Description != nil {
					projected["description"] = *style.Description
				}
				items = append(items, projected)
			}
		}
	case ToolFormatList:
		page, err := tools.formats.ListFormatsAdmin(ctx, query)
		if err != nil {
			return expectedToolError(err)
		}
		responsePagination = page.Pagination
		for _, item := range page.Items {
			if item != nil && item.Format != nil {
				format := item.Format
				items = append(items, map[string]any{"id": format.Id, "name": format.Name, "slug": format.Slug})
			}
		}
	}
	output := map[string]any{"items": items, "total": responsePagination.GetTotal(), "limit": limit, "offset": input.Offset, "has_more": responsePagination.GetHasMore()}
	if responsePagination.GetHasMore() {
		output["next_offset"] = int64(input.Offset) + int64(limit)
	}
	return contentResult(output)
}
