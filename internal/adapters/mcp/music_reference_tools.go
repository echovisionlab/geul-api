package mcp

import (
	"context"
	"errors"

	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
)

const (
	ToolGenreList  = "genre_list"
	ToolStyleList  = "style_list"
	ToolFormatList = "format_list"
)

var musicReferenceTools = toolRegistry[MusicReferenceTools]{
	defineTool(oauthTool(ToolGenreList, "List Genres", "List or search canonical Genre IDs and native catalog names before selecting Release genre relations. Continue with next_offset and the same query. Authorization remains in the native admin list.", musicReferenceListInputJSONSchema, musicNamedReferenceListOutputJSONSchema, true, false), (*MusicReferenceTools).list),
	defineTool(oauthTool(ToolStyleList, "List Styles", "List or search canonical Style IDs and native catalog names before selecting Release style relations. Continue with next_offset and the same query. Authorization remains in the native admin list.", musicReferenceListInputJSONSchema, musicNamedReferenceListOutputJSONSchema, true, false), (*MusicReferenceTools).list),
	defineTool(oauthTool(ToolFormatList, "List Formats", "List or search canonical Format IDs and native catalog names before selecting Release format relations. Continue with next_offset and the same query. Authorization remains in the native admin list.", musicReferenceListInputJSONSchema, musicFormatListOutputJSONSchema, true, false), (*MusicReferenceTools).list),
}

type GenreReferenceDiscovery interface {
	ListGenresAdmin(context.Context, referencecatalog.ReferenceListQuery) (referencecatalog.Page[referencecatalog.ReferenceUsage], error)
}

type StyleReferenceDiscovery interface {
	ListStylesAdmin(context.Context, referencecatalog.ReferenceListQuery) (referencecatalog.Page[referencecatalog.ReferenceUsage], error)
}

type FormatReferenceDiscovery interface {
	ListFormatsAdmin(context.Context, referencecatalog.ReferenceListQuery) (referencecatalog.Page[referencecatalog.ReferenceUsage], error)
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

func (*MusicReferenceTools) ToolNames() []string { return musicReferenceTools.names() }

func (*MusicReferenceTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return musicReferenceTools.list(), nil
}

func (tools *MusicReferenceTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	return musicReferenceTools.call(tools, ctx, name, arguments)
}

func musicReferenceResult(query referencecatalog.ReferenceListQuery, pagination referencecatalog.Pagination, items []map[string]any) (mcpserver.ToolResult, error) {
	output := map[string]any{"items": items, "total": pagination.Total, "limit": query.Limit, "offset": query.Offset, "has_more": pagination.HasMore}
	if pagination.HasMore {
		output["next_offset"] = int64(query.Offset) + int64(query.Limit)
	}
	return contentResult(output)
}

// musicReferenceListQuery reads the query, limit (20 when absent) and offset
// of a music reference list.
func musicReferenceListQuery(arguments mcpserver.ToolArguments) (referencecatalog.ReferenceListQuery, error) {
	if err := rejectNullArguments(arguments, "query", "limit", "offset"); err != nil {
		return referencecatalog.ReferenceListQuery{}, err
	}
	var input musicReferenceListArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return referencecatalog.ReferenceListQuery{}, err
	}
	limit := int32(20)
	if input.Limit != nil {
		limit = *input.Limit
	}
	if limit < 1 || limit > 100 {
		return referencecatalog.ReferenceListQuery{}, errors.New("limit must be between 1 and 100")
	}
	if input.Offset < 0 {
		return referencecatalog.ReferenceListQuery{}, errors.New("offset must be between 0 and 2147483647")
	}
	return referencecatalog.ReferenceListQuery{Filters: discoverySearchFilters(input.Query), Limit: limit, Offset: input.Offset}, nil
}

// projectMusicReferences projects the listed references.
func projectMusicReferences(listed []referencecatalog.ReferenceUsage, project func(referencecatalog.Reference) map[string]any) []map[string]any {
	items := make([]map[string]any, 0, len(listed))
	for _, item := range listed {
		items = append(items, project(item.Reference))
	}
	return items
}

// describedMusicReference projects a Genre or Style.
func describedMusicReference(reference referencecatalog.Reference) map[string]any {
	projected := map[string]any{"id": reference.ID, "name": reference.Name, "slug": reference.Slug}
	if reference.Description != nil {
		projected["description"] = *reference.Description
	}
	return projected
}

func projectFormatReference(format referencecatalog.Reference) map[string]any {
	return map[string]any{"id": format.ID, "name": format.Name, "slug": format.Slug}
}

func (tools *MusicReferenceTools) list(ctx context.Context, name string, call toolCall[musicReferenceListArguments]) (mcpserver.ToolResult, error) {
	arguments := call.Arguments
	query, err := musicReferenceListQuery(arguments)
	if err != nil {
		return executionError(err)
	}
	switch name {
	case ToolGenreList:
		page, err := tools.genres.ListGenresAdmin(ctx, query)
		if err != nil {
			return expectedToolError(err)
		}
		return musicReferenceResult(query, page.Pagination, projectMusicReferences(page.Items, describedMusicReference))
	case ToolStyleList:
		page, err := tools.styles.ListStylesAdmin(ctx, query)
		if err != nil {
			return expectedToolError(err)
		}
		return musicReferenceResult(query, page.Pagination, projectMusicReferences(page.Items, describedMusicReference))
	default:
		page, err := tools.formats.ListFormatsAdmin(ctx, query)
		if err != nil {
			return expectedToolError(err)
		}
		return musicReferenceResult(query, page.Pagination, projectMusicReferences(page.Items, projectFormatReference))
	}
}

type musicReferenceListArguments = struct {
	Query  string `json:"query,omitempty"`
	Limit  *int32 `json:"limit,omitempty"`
	Offset int32  `json:"offset,omitempty"`
}
