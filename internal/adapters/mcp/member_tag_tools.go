package mcp

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const ToolMemberTagList = "member_tag_list"

var memberTagTools = []mcpserver.Tool{
	relatedTool(ToolMemberTagList, "List Member tags", "List or search administrator-only Member tag IDs, names, and member counts. Use returned IDs to interpret tag_ids from member_admin_get or member_admin_list. Continue with the same query and next_offset. Authorization remains in the owning Member service.", memberTagListInputJSONSchema, memberTagListOutputJSONSchema, true, false),
}

type MemberTagReader interface {
	ListMemberTagsAdmin(context.Context, *connect.Request[managev1.ListMemberTagsAdminRequest]) (*connect.Response[managev1.ListMemberTagsAdminResponse], error)
}

type MemberTagTools struct{ reader MemberTagReader }

func NewMemberTagTools(reader MemberTagReader) (*MemberTagTools, error) {
	if interfaceValueIsNil(reader) {
		return nil, errors.New("MCP administrator Member tag reader is required")
	}
	return &MemberTagTools{reader: reader}, nil
}

func (*MemberTagTools) ToolNames() []string { return toolDefinitionNames(memberTagTools) }

func (*MemberTagTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return cloneToolDefinitions(memberTagTools), nil
}

func (tools *MemberTagTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	if name != ToolMemberTagList {
		return mcpserver.ToolResult{}, mcpserver.ErrUnknownTool
	}
	if err := rejectNullArguments(arguments, "query", "limit", "offset"); err != nil {
		return executionError(err)
	}
	var input struct {
		Query  string `json:"query"`
		Limit  *int32 `json:"limit"`
		Offset int32  `json:"offset"`
	}
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	limit := int32(50)
	if input.Limit != nil {
		limit = *input.Limit
	}
	if limit < 1 || limit > 500 {
		return executionError(errors.New("limit must be between 1 and 500"))
	}
	if input.Offset < 0 {
		return executionError(errors.New("offset must be between 0 and 2147483647"))
	}
	response, err := tools.reader.ListMemberTagsAdmin(ctx, connect.NewRequest(&managev1.ListMemberTagsAdminRequest{
		Pagination: &commonv1.PaginationRequest{Limit: limit, Offset: input.Offset},
		Filters:    discoverySearchFilters(input.Query),
	}))
	if err != nil {
		return expectedToolError(err)
	}
	items := make([]map[string]any, 0, len(response.Msg.Tags))
	for _, tag := range response.Msg.Tags {
		items = append(items, map[string]any{"id": tag.Id, "name": tag.Name, "member_count": tag.MemberCount})
	}
	pagination := response.Msg.Pagination
	output := map[string]any{"items": items, "total": pagination.GetTotal(), "limit": pagination.GetLimit(), "offset": pagination.GetOffset(), "has_more": pagination.GetHasMore()}
	if pagination.GetHasMore() {
		output["next_offset"] = int64(pagination.GetOffset()) + int64(pagination.GetLimit())
	}
	return contentResult(output)
}
