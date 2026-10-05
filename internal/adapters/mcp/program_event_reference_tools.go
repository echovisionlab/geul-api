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
	ToolProgramEventTypeList   = "program_event_type_list"
	ToolProgramEventSeriesList = "program_event_series_list"
	ToolLabelList              = "label_list"
)

var programEventReferenceTools = []mcpserver.Tool{
	relatedTool(ToolProgramEventTypeList, "List Program Event types", "List canonical Program Event type IDs, localized names, status, and place/stream requirements before program_event_create or program_event_settings_update. Choose an active type and pass its id as type_id. Type name search is not supported; continue with next_offset to see more types. Authorization remains in the native admin list.", programEventTypeListInputJSONSchema, programEventTypeListOutputJSONSchema, true, false),
	relatedTool(ToolProgramEventSeriesList, "List Program Event series", "List or search canonical Program Event series IDs and titles. Pass a selected id as the optional series_id in program_event_create or program_event_settings_update. Continue with next_offset and the same filters. Authorization remains in the native admin list.", programEventSeriesListInputJSONSchema, programEventSeriesListOutputJSONSchema, true, false),
	relatedTool(ToolLabelList, "List Labels", "List or search canonical Label IDs and source names. Use returned IDs for optional Program Event label relations. Continue with next_offset and the same filters. Authorization remains in the native admin list.", labelListInputJSONSchema, labelListOutputJSONSchema, true, false),
}

// These interfaces consume only the native authorized reference lists.
type ProgramEventTypeReferenceDiscovery interface {
	ListProgramEventTypesAdmin(context.Context, *connect.Request[managev1.ListProgramEventTypesAdminRequest]) (*connect.Response[managev1.ListProgramEventTypesAdminResponse], error)
}

type ProgramEventSeriesReferenceDiscovery interface {
	ListProgramEventSeriesAdmin(context.Context, *connect.Request[managev1.ListProgramEventSeriesAdminRequest]) (*connect.Response[managev1.ListProgramEventSeriesAdminResponse], error)
}

type LabelReferenceDiscovery interface {
	ListLabelsAdmin(context.Context, *connect.Request[managev1.ListLabelsAdminRequest]) (*connect.Response[managev1.ListLabelsAdminResponse], error)
}

type ProgramEventReferenceTools struct {
	types  ProgramEventTypeReferenceDiscovery
	series ProgramEventSeriesReferenceDiscovery
	labels LabelReferenceDiscovery
}

func NewProgramEventReferenceTools(types ProgramEventTypeReferenceDiscovery, series ProgramEventSeriesReferenceDiscovery, labels LabelReferenceDiscovery) (*ProgramEventReferenceTools, error) {
	if interfaceValueIsNil(types) || interfaceValueIsNil(series) || interfaceValueIsNil(labels) {
		return nil, errors.New("MCP Program Event reference discovery applications are required")
	}
	return &ProgramEventReferenceTools{types: types, series: series, labels: labels}, nil
}

func (*ProgramEventReferenceTools) ToolNames() []string {
	return toolDefinitionNames(programEventReferenceTools)
}

func (*ProgramEventReferenceTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return cloneToolDefinitions(programEventReferenceTools), nil
}

type programEventReferenceListArguments struct {
	Status string `json:"status,omitempty"`
	Limit  *int32 `json:"limit,omitempty"`
	Offset int32  `json:"offset,omitempty"`
}

type programEventReferenceSearchArguments struct {
	programEventReferenceListArguments
	Query string `json:"query,omitempty"`
}

func (input programEventReferenceListArguments) pagination() (*commonv1.PaginationRequest, error) {
	limit := int32(20)
	if input.Limit != nil {
		limit = *input.Limit
	}
	if limit < 1 || limit > 100 {
		return nil, errors.New("limit must be between 1 and 100")
	}
	if input.Offset < 0 {
		return nil, errors.New("offset must be between 0 and 2147483647")
	}
	return &commonv1.PaginationRequest{Limit: limit, Offset: input.Offset}, nil
}

func (tools *ProgramEventReferenceTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	if name != ToolProgramEventTypeList && name != ToolProgramEventSeriesList && name != ToolLabelList {
		return mcpserver.ToolResult{}, mcpserver.ErrUnknownTool
	}
	if err := rejectNullArguments(arguments, "query", "status", "limit", "offset"); err != nil {
		return executionError(err)
	}
	var input programEventReferenceSearchArguments
	var err error
	if name == ToolProgramEventTypeList {
		// Types have no native name search; do not accept an ignored query.
		err = decodeArguments(arguments, &input.programEventReferenceListArguments)
	} else {
		err = decodeArguments(arguments, &input)
	}
	if err != nil {
		return executionError(err)
	}
	pagination, err := input.pagination()
	if err != nil {
		return executionError(err)
	}
	filters := discoverySearchFilters(input.Query)
	if _, supplied := arguments["status"]; supplied {
		valid := false
		switch name {
		case ToolProgramEventTypeList:
			valid = input.Status == managev1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_ACTIVE.String() || input.Status == managev1.ProgramEventTypeStatus_PROGRAM_EVENT_TYPE_STATUS_INACTIVE.String()
		case ToolProgramEventSeriesList:
			valid = input.Status == managev1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_DRAFT.String() || input.Status == managev1.ProgramEventSeriesStatus_PROGRAM_EVENT_SERIES_STATUS_PUBLISHED.String()
		case ToolLabelList:
			valid = input.Status == managev1.LabelStatus_LABEL_STATUS_DRAFT.String() || input.Status == managev1.LabelStatus_LABEL_STATUS_PUBLISHED.String()
		}
		if !valid {
			return executionError(fmt.Errorf("unsupported status %q for %s", input.Status, name))
		}
		filters = append(filters, &commonv1.FilterSpec{Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: input.Status})
	}
	items := make([]map[string]any, 0)
	var responsePagination *commonv1.PaginationResponse
	switch name {
	case ToolProgramEventTypeList:
		response, err := tools.types.ListProgramEventTypesAdmin(ctx, connect.NewRequest(&managev1.ListProgramEventTypesAdminRequest{Pagination: pagination, Filters: filters}))
		if err != nil {
			return expectedToolError(err)
		}
		responsePagination = response.Msg.Pagination
		for _, item := range response.Msg.Types {
			if item == nil {
				continue
			}
			locales := make([]map[string]any, 0, len(item.Locales))
			for _, locale := range item.Locales {
				if locale != nil {
					localized := map[string]any{"locale": locale.Locale, "name": locale.Name}
					if locale.Description != nil {
						localized["description"] = *locale.Description
					}
					locales = append(locales, localized)
				}
			}
			items = append(items, map[string]any{"id": item.Id, "slug": item.Slug, "status": item.Status.String(), "requires_place": item.RequiresPlace, "requires_stream_url": item.RequiresStreamUrl, "locales": locales})
		}
	case ToolProgramEventSeriesList:
		response, err := tools.series.ListProgramEventSeriesAdmin(ctx, connect.NewRequest(&managev1.ListProgramEventSeriesAdminRequest{Pagination: pagination, Filters: filters}))
		if err != nil {
			return expectedToolError(err)
		}
		responsePagination = response.Msg.Pagination
		for _, item := range response.Msg.Series {
			if item != nil {
				items = append(items, map[string]any{"id": item.Id, "slug": item.Slug, "title": item.Title, "status": item.Status.String()})
			}
		}
	case ToolLabelList:
		response, err := tools.labels.ListLabelsAdmin(ctx, connect.NewRequest(&managev1.ListLabelsAdminRequest{Pagination: pagination, Filters: filters}))
		if err != nil {
			return expectedToolError(err)
		}
		responsePagination = response.Msg.Pagination
		for _, item := range response.Msg.Labels {
			if item != nil && item.Label != nil {
				label := item.Label
				projected := map[string]any{"id": label.Id, "name": label.Name, "status": label.Status, "source_locale": label.SourceLocale}
				if label.Slug != nil {
					projected["slug"] = *label.Slug
				}
				items = append(items, projected)
			}
		}
	}
	output := map[string]any{"items": items, "total": responsePagination.GetTotal(), "limit": pagination.Limit, "offset": pagination.Offset, "has_more": responsePagination.GetHasMore()}
	if responsePagination.GetHasMore() {
		output["next_offset"] = int64(pagination.Offset) + int64(pagination.Limit)
	}
	return contentResult(output)
}
