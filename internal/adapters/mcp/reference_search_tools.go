package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
)

const ToolReferenceSearch = "reference_search"

var referenceSearchTools = toolRegistry[ReferenceSearchTools]{
	defineTool(oauthTool(ToolReferenceSearch, "Find references",
		"Find the IDs of what documents refer to: categories, tags, clients, map places, members, artists, genres, styles, formats, Program Event types and series, and Member tags. Authorization remains in each owning list.",
		referenceLookupInputJSONSchema, referenceLookupOutputJSONSchema, true, false), (*ReferenceSearchTools).search),
}

// ReferenceSearchApplications holds the use cases that own the reference lists. Each
// one still decides who may read it.
type ReferenceSearchApplications struct {
	Categories  CategoryReferenceDiscovery
	Tags        TagReferenceDiscovery
	Clients     ClientReferenceDiscovery
	MapPlaces   MapPlaceReferenceDiscovery
	Members     MemberReferenceDiscovery
	Artists     ArtistReferenceDiscovery
	Genres      GenreReferenceDiscovery
	Styles      StyleReferenceDiscovery
	Formats     FormatReferenceDiscovery
	EventTypes  ProgramEventTypeReferenceDiscovery
	EventSeries ProgramEventSeriesReferenceDiscovery
	MemberTags  MemberTagReader
}

func (applications ReferenceSearchApplications) complete() bool {
	return !slices.ContainsFunc([]any{
		applications.Categories, applications.Tags, applications.Clients, applications.MapPlaces, applications.Members, applications.Artists,
		applications.Genres, applications.Styles, applications.Formats, applications.EventTypes, applications.EventSeries, applications.MemberTags,
	}, interfaceValueIsNil)
}

// ReferenceSearchTools answers every reference lookup through one tool.
type ReferenceSearchTools struct {
	quickSearches map[string]quickReferenceSearch
	pagedLists    map[string]pagedReferenceList
}

func NewReferenceSearchTools(applications ReferenceSearchApplications) (*ReferenceSearchTools, error) {
	if !applications.complete() {
		return nil, errors.New("MCP reference search needs the application of every reference list")
	}
	return &ReferenceSearchTools{quickSearches: newQuickReferenceSearches(applications), pagedLists: newPagedReferenceLists(applications)}, nil
}

func (*ReferenceSearchTools) ToolNames() []string { return referenceSearchTools.names() }

func (*ReferenceSearchTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return referenceSearchTools.list(), nil
}

func (tools *ReferenceSearchTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	return referenceSearchTools.call(tools, ctx, name, arguments)
}

type referenceSearchArguments struct {
	ReferenceType string  `json:"reference_type"`
	Query         *string `json:"query,omitempty"`
	Limit         *int32  `json:"limit,omitempty"`
	Offset        *int32  `json:"offset,omitempty"`
	Status        *string `json:"status,omitempty"`
}

// query is the search text, empty when none was given.
func (input referenceSearchArguments) query() string {
	if input.Query == nil {
		return ""
	}
	return *input.Query
}

// limit is the requested page size, within maximum.
func (input referenceSearchArguments) limit(maximum int32) (int32, error) {
	if input.Limit == nil {
		return referenceSearchDefaultLimit, nil
	}
	if *input.Limit < 1 || *input.Limit > maximum {
		return 0, fmt.Errorf("limit must be between 1 and %d for %s references", maximum, input.ReferenceType)
	}
	return *input.Limit, nil
}

const (
	referenceSearchDefaultLimit int32 = 20
	referenceSearchMaximumLimit int32 = 100
)

// referenceSearchOutput is every reference type's answer. HasMore is null when a
// quick search reached its service cap, so whether more exist is unknown.
type referenceSearchOutput struct {
	ReferenceType string           `json:"reference_type"`
	Items         []map[string]any `json:"items"`
	Count         int              `json:"count"`
	Total         *int64           `json:"total,omitempty"`
	HasMore       *bool            `json:"has_more"`
	NextOffset    *int64           `json:"next_offset,omitempty"`
}

func (tools *ReferenceSearchTools) search(ctx context.Context, _ string, call toolCall[referenceSearchArguments]) (mcpserver.ToolResult, error) {
	if err := rejectNullArguments(call.Arguments, "query", "limit", "offset", "status"); err != nil {
		return executionError(err)
	}
	var input referenceSearchArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	output, err := tools.read(ctx, input)
	if err != nil {
		return expectedToolError(err)
	}
	output.ReferenceType, output.Count = input.ReferenceType, len(output.Items)
	encoded, err := json.Marshal(output)
	if err != nil {
		return mcpserver.ToolResult{}, fmt.Errorf("encode MCP reference search: %w", err)
	}
	return structuredResult(encoded, false)
}

func (tools *ReferenceSearchTools) read(ctx context.Context, input referenceSearchArguments) (referenceSearchOutput, error) {
	if list, paged := tools.pagedLists[input.ReferenceType]; paged {
		return list.page(ctx, input)
	}
	if search, quick := tools.quickSearches[input.ReferenceType]; quick {
		return search.run(ctx, input)
	}
	return referenceSearchOutput{}, &core.InputError{Message: fmt.Sprintf("unsupported reference_type %q", input.ReferenceType)}
}

var (
	_ mcpserver.ToolRegistry   = (*ReferenceSearchTools)(nil)
	_ mcpserver.ToolDispatcher = (*ReferenceSearchTools)(nil)
	_ ToolProvider             = (*ReferenceSearchTools)(nil)
)
