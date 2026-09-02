package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const ToolDocumentList = "document_list"

var documentDiscoveryTools = []mcpserver.Tool{{
	Name:  ToolDocumentList,
	Title: "List accessible documents",
	Description: "List Post, Work, Page, or Program Event documents the authenticated Geul member may open. " +
		"Use this before document_open when the document UUID is unknown. " +
		"Pass the returned d unchanged to document_open; a slug or URL is not a document ID.",
	InputSchema:     json.RawMessage(documentListInputJSONSchema),
	OutputSchema:    json.RawMessage(documentListOutputJSONSchema),
	SecuritySchemes: oauthSecuritySchemes(),
	Annotations:     toolAnnotations(true, false, false),
	Meta:            oauthSecurityMeta(),
}}

// PostDocumentDiscovery is the exact Post-owned read capability consumed by
// the MCP discovery adapter. Authorization remains in the Post service.
type PostDocumentDiscovery interface {
	ListAIDocuments(context.Context, postdomain.AIDocumentListInput) (postdomain.AIDocumentListResult, error)
}

type WorkDocumentDiscovery interface {
	ListWorksAdmin(context.Context, *connect.Request[managev1.ListWorksAdminRequest]) (*connect.Response[managev1.ListWorksAdminResponse], error)
}

type PageDocumentDiscovery interface {
	ListPagesAdmin(context.Context, *connect.Request[managev1.ListPagesAdminRequest]) (*connect.Response[managev1.ListPagesAdminResponse], error)
}

type ProgramEventDocumentDiscovery interface {
	ListProgramEventsAdmin(context.Context, *connect.Request[managev1.ListProgramEventsAdminRequest]) (*connect.Response[managev1.ListProgramEventsAdminResponse], error)
}

// DocumentDiscoveryTools owns only document selection metadata. DCDP reads
// and mutations remain in AIDocumentTools.
type DocumentDiscoveryTools struct {
	posts         PostDocumentDiscovery
	works         WorkDocumentDiscovery
	pages         PageDocumentDiscovery
	programEvents ProgramEventDocumentDiscovery
}

func NewDocumentDiscoveryTools(
	posts PostDocumentDiscovery,
	works WorkDocumentDiscovery,
	pages PageDocumentDiscovery,
	programEvents ProgramEventDocumentDiscovery,
) (*DocumentDiscoveryTools, error) {
	if interfaceValueIsNil(posts) {
		return nil, errors.New("MCP Post document discovery is required")
	}
	if interfaceValueIsNil(works) {
		return nil, errors.New("MCP Work document discovery is required")
	}
	if interfaceValueIsNil(pages) {
		return nil, errors.New("MCP Page document discovery is required")
	}
	if interfaceValueIsNil(programEvents) {
		return nil, errors.New("MCP Program Event document discovery is required")
	}
	return &DocumentDiscoveryTools{posts: posts, works: works, pages: pages, programEvents: programEvents}, nil
}

func (*DocumentDiscoveryTools) ToolNames() []string {
	return toolDefinitionNames(documentDiscoveryTools)
}

func (*DocumentDiscoveryTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return cloneToolDefinitions(documentDiscoveryTools), nil
}

func (tools *DocumentDiscoveryTools) CallTool(
	ctx context.Context,
	_ mcpserver.Principal,
	name string,
	arguments mcpserver.ToolArguments,
) (mcpserver.ToolResult, error) {
	if name != ToolDocumentList {
		return mcpserver.ToolResult{}, mcpserver.ErrUnknownTool
	}
	var input struct {
		Profile string `json:"p"`
		Query   string `json:"q,omitempty"`
		Limit   int    `json:"limit,omitempty"`
		Offset  int    `json:"offset,omitempty"`
	}
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	if input.Profile != "post" && input.Profile != "work" && input.Profile != "page" && input.Profile != "program_event" {
		return executionError(fmt.Errorf("p must be post, work, page, or program_event"))
	}
	documents, total, limit, err := tools.listDocuments(ctx, input.Profile, input.Query, input.Limit, input.Offset)
	if err != nil {
		return expectedToolError(err)
	}
	var nextOffset any
	if int64(input.Offset+limit) < total {
		nextOffset = input.Offset + limit
	}
	encoded, err := json.Marshal(map[string]any{
		"documents":   documents,
		"total":       total,
		"next_offset": nextOffset,
	})
	if err != nil {
		return mcpserver.ToolResult{}, fmt.Errorf("encode MCP document list: %w", err)
	}
	return structuredResult(encoded, false)
}

func (tools *DocumentDiscoveryTools) listDocuments(
	ctx context.Context,
	profile string,
	query string,
	limit int,
	offset int,
) ([]map[string]any, int64, int, error) {
	if limit == 0 {
		limit = 20
	}
	switch profile {
	case "post":
		listed, err := tools.posts.ListAIDocuments(ctx, postdomain.AIDocumentListInput{
			Query: query, Limit: limit, Offset: offset,
		})
		if err != nil {
			return nil, 0, 0, err
		}
		documents := make([]map[string]any, len(listed.Items))
		for index, item := range listed.Items {
			documents[index] = discoveryDocument("post", item.ID, item.Title, item.Slug, item.SourceLocale, item.Status, item.UpdatedAt)
		}
		return documents, listed.Total, listed.Limit, nil
	case "work":
		request := connect.NewRequest(&managev1.ListWorksAdminRequest{
			Pagination: &commonv1.PaginationRequest{Limit: int32(limit), Offset: int32(offset)},
			Filters:    discoverySearchFilters(query),
			Sorts:      []*commonv1.SortSpec{{Field: "updated_at", Order: commonv1.SortOrder_SORT_ORDER_DESC}},
		})
		listed, err := tools.works.ListWorksAdmin(ctx, request)
		if err != nil {
			return nil, 0, 0, err
		}
		documents := make([]map[string]any, 0, len(listed.Msg.Works))
		for _, item := range listed.Msg.Works {
			if item == nil || item.Work == nil || item.Work.UpdatedAt == nil {
				continue
			}
			work := item.Work
			documents = append(documents, discoveryDocument(
				"work", work.Id, work.Title, work.Slug, work.SourceLocale,
				strings.TrimPrefix(work.Status.String(), "WORK_STATUS_"), work.UpdatedAt.AsTime(),
			))
		}
		return documents, int64(listed.Msg.Pagination.GetTotal()), int(listed.Msg.Pagination.GetLimit()), nil
	case "page":
		request := connect.NewRequest(&managev1.ListPagesAdminRequest{
			Pagination: &commonv1.PaginationRequest{Limit: int32(limit), Offset: int32(offset)},
			Filters:    discoverySearchFilters(query),
			Sorts:      []*commonv1.SortSpec{{Field: "updated_at", Order: commonv1.SortOrder_SORT_ORDER_DESC}},
		})
		listed, err := tools.pages.ListPagesAdmin(ctx, request)
		if err != nil {
			return nil, 0, 0, err
		}
		documents := make([]map[string]any, 0, len(listed.Msg.Pages))
		for _, page := range listed.Msg.Pages {
			if page == nil || page.UpdatedAt == nil {
				continue
			}
			documents = append(documents, discoveryDocument(
				"page", page.Id, page.Title, page.Slug, page.SourceLocale,
				strings.TrimPrefix(page.Status.String(), "PAGE_STATUS_"), page.UpdatedAt.AsTime(),
			))
		}
		return documents, int64(listed.Msg.Pagination.GetTotal()), int(listed.Msg.Pagination.GetLimit()), nil
	case "program_event":
		request := connect.NewRequest(&managev1.ListProgramEventsAdminRequest{
			Pagination: &commonv1.PaginationRequest{Limit: int32(limit), Offset: int32(offset)},
			Filters:    discoverySearchFilters(query),
			Sorts:      []*commonv1.SortSpec{{Field: "updated_at", Order: commonv1.SortOrder_SORT_ORDER_DESC}},
		})
		listed, err := tools.programEvents.ListProgramEventsAdmin(ctx, request)
		if err != nil {
			return nil, 0, 0, err
		}
		documents := make([]map[string]any, 0, len(listed.Msg.Events))
		for _, event := range listed.Msg.Events {
			if event == nil || event.UpdatedAt == nil {
				continue
			}
			documents = append(documents, discoveryDocument(
				"program_event", event.Id, event.Title, event.Slug, event.SourceLocale,
				strings.TrimPrefix(event.Status.String(), "PROGRAM_EVENT_STATUS_"), event.UpdatedAt.AsTime(),
			))
		}
		return documents, int64(listed.Msg.Pagination.GetTotal()), int(listed.Msg.Pagination.GetLimit()), nil
	default:
		return nil, 0, 0, fmt.Errorf("p must be post, work, page, or program_event")
	}
}

func discoverySearchFilters(query string) []*commonv1.FilterSpec {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	return []*commonv1.FilterSpec{{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: query}}
}

func discoveryDocument(profile, id, title string, slug *string, sourceLocale, status string, updatedAt time.Time) map[string]any {
	status = strings.TrimPrefix(status, "POST_STATUS_")
	status = strings.TrimPrefix(status, "WORK_STATUS_")
	status = strings.TrimPrefix(status, "PAGE_STATUS_")
	status = strings.TrimPrefix(status, "PROGRAM_EVENT_STATUS_")
	document := map[string]any{
		"p": profile, "d": id, "title": title,
		"source_locale": sourceLocale, "status": strings.ToLower(status),
		"updated_at": updatedAt.UTC().Format(time.RFC3339),
	}
	if slug != nil {
		document["slug"] = *slug
	}
	return document
}

var (
	_ mcpserver.ToolRegistry   = (*DocumentDiscoveryTools)(nil)
	_ mcpserver.ToolDispatcher = (*DocumentDiscoveryTools)(nil)
	_ ToolProvider             = (*DocumentDiscoveryTools)(nil)
)
