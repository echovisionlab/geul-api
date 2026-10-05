package mcp

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const (
	ToolFormList       = "form_list"
	ToolPostSeriesList = "post_series_list"
)

var pageReferenceTools = []mcpserver.Tool{
	relatedTool(ToolFormList, "List Forms", "List or search canonical Form IDs and source titles before editing a Page form section. Pass a selected id unchanged as form_id. Native status is a snapshot, not a guarantee of public form access. Continue with next_offset and the same filters. Authorization remains in the native admin list; form bodies and submissions are omitted.", formListInputJSONSchema, formListOutputJSONSchema, true, false),
	relatedTool(ToolPostSeriesList, "List Post series", "List or search canonical Post series IDs and source titles before editing Page post list, table, or map sections. Pass a selected id unchanged as series_id. Continue with next_offset and the same filters. Authorization remains in the native admin list; content and managers are omitted.", postSeriesListInputJSONSchema, postSeriesListOutputJSONSchema, true, false),
}

type FormReferenceDiscovery interface {
	ListFormsAdmin(context.Context, *connect.Request[managev1.ListFormsAdminRequest]) (*connect.Response[managev1.ListFormsAdminResponse], error)
}

type PostSeriesReferenceDiscovery interface {
	ListSeriesAdmin(context.Context, *connect.Request[managev1.ListSeriesAdminRequest]) (*connect.Response[managev1.ListSeriesAdminResponse], error)
}

type PageReferenceTools struct {
	forms  FormReferenceDiscovery
	series PostSeriesReferenceDiscovery
}

func NewPageReferenceTools(forms FormReferenceDiscovery, series PostSeriesReferenceDiscovery) (*PageReferenceTools, error) {
	if interfaceValueIsNil(forms) || interfaceValueIsNil(series) {
		return nil, errors.New("MCP Page reference discovery applications are required")
	}
	return &PageReferenceTools{forms: forms, series: series}, nil
}

func (*PageReferenceTools) ToolNames() []string {
	return toolDefinitionNames(pageReferenceTools)
}

func (*PageReferenceTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return cloneToolDefinitions(pageReferenceTools), nil
}

func (tools *PageReferenceTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	if name != ToolFormList && name != ToolPostSeriesList {
		return mcpserver.ToolResult{}, mcpserver.ErrUnknownTool
	}
	if err := rejectNullArguments(arguments, "query", "status", "limit", "offset"); err != nil {
		return executionError(err)
	}
	var input struct {
		Query  string `json:"query,omitempty"`
		Status string `json:"status,omitempty"`
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
	pagination := &commonv1.PaginationRequest{Limit: limit, Offset: input.Offset}
	filters := discoverySearchFilters(input.Query)
	if _, supplied := arguments["status"]; supplied {
		valid := false
		switch name {
		case ToolFormList:
			valid = input.Status == managev1.FormStatus_FORM_STATUS_DRAFT.String() || input.Status == managev1.FormStatus_FORM_STATUS_PUBLISHED.String()
		case ToolPostSeriesList:
			valid = input.Status == managev1.SeriesStatus_SERIES_STATUS_DRAFT.String() || input.Status == managev1.SeriesStatus_SERIES_STATUS_PUBLISHED.String()
		}
		if !valid {
			return executionError(fmt.Errorf("unsupported status %q for %s", input.Status, name))
		}
		filters = append(filters, &commonv1.FilterSpec{Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: input.Status})
	}
	items := make([]map[string]any, 0)
	var responsePagination *commonv1.PaginationResponse
	switch name {
	case ToolFormList:
		response, err := tools.forms.ListFormsAdmin(ctx, connect.NewRequest(&managev1.ListFormsAdminRequest{Pagination: pagination, Filters: filters}))
		if err != nil {
			return expectedToolError(err)
		}
		responsePagination = response.Msg.Pagination
		for _, item := range response.Msg.Forms {
			if item != nil {
				projected := map[string]any{"id": item.Id, "title": item.Title, "status": item.Status.String(), "source_locale": item.SourceLocale}
				if item.Slug != nil {
					projected["slug"] = *item.Slug
				}
				items = append(items, projected)
			}
		}
	case ToolPostSeriesList:
		response, err := tools.series.ListSeriesAdmin(ctx, connect.NewRequest(&managev1.ListSeriesAdminRequest{Pagination: pagination, Filters: filters}))
		if err != nil {
			return expectedToolError(err)
		}
		responsePagination = response.Msg.Pagination
		for _, item := range response.Msg.Series {
			if item != nil && item.Series != nil {
				series := item.Series
				items = append(items, map[string]any{"id": series.Id, "title": series.Title, "slug": series.Slug, "status": series.Status, "source_locale": series.SourceLocale, "post_count": item.PostCount})
			}
		}
	}
	output := map[string]any{"items": items, "total": responsePagination.GetTotal(), "limit": pagination.Limit, "offset": pagination.Offset, "has_more": responsePagination.GetHasMore()}
	if responsePagination.GetHasMore() {
		output["next_offset"] = int64(pagination.Offset) + int64(pagination.Limit)
	}
	return contentResult(output)
}
