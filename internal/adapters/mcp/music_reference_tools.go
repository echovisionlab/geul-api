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
		return musicReferenceResult(query, page.Pagination, projectMusicReferences(page.Items, projectGenreReference))
	case ToolStyleList:
		page, err := tools.styles.ListStylesAdmin(ctx, query)
		if err != nil {
			return expectedToolError(err)
		}
		return musicReferenceResult(query, page.Pagination, projectMusicReferences(page.Items, projectStyleReference))
	default:
		page, err := tools.formats.ListFormatsAdmin(ctx, query)
		if err != nil {
			return expectedToolError(err)
		}
		return musicReferenceResult(query, page.Pagination, projectMusicReferences(page.Items, projectFormatReference))
	}
}

func musicReferenceResult(query referencecatalog.ReferenceListQuery, pagination *commonv1.PaginationResponse, items []map[string]any) (mcpserver.ToolResult, error) {
	output := map[string]any{"items": items, "total": pagination.GetTotal(), "limit": query.Limit, "offset": query.Offset, "has_more": pagination.GetHasMore()}
	if pagination.GetHasMore() {
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
	var input struct {
		Query  string `json:"query,omitempty"`
		Limit  *int32 `json:"limit,omitempty"`
		Offset int32  `json:"offset,omitempty"`
	}
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

// projectMusicReferences projects the listed references; project answers nil
// for an empty entry, which is skipped.
func projectMusicReferences[T any](listed []T, project func(T) map[string]any) []map[string]any {
	items := make([]map[string]any, 0, len(listed))
	for _, item := range listed {
		if projected := project(item); projected != nil {
			items = append(items, projected)
		}
	}
	return items
}

func projectGenreReference(item *managev1.GenreWithStats) map[string]any {
	if genre := item.GetGenre(); genre != nil {
		return describedMusicReference(genre.Id, genre.Name, genre.Slug, genre.Description)
	}
	return nil
}

func projectStyleReference(item *managev1.StyleWithStats) map[string]any {
	if style := item.GetStyle(); style != nil {
		return describedMusicReference(style.Id, style.Name, style.Slug, style.Description)
	}
	return nil
}

func projectFormatReference(item *managev1.FormatWithStats) map[string]any {
	if format := item.GetFormat(); format != nil {
		return map[string]any{"id": format.Id, "name": format.Name, "slug": format.Slug}
	}
	return nil
}

func describedMusicReference(id, name, slug string, description *string) map[string]any {
	projected := map[string]any{"id": id, "name": name, "slug": slug}
	if description != nil {
		projected["description"] = *description
	}
	return projected
}
