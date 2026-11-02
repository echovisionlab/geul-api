package mcp

import (
	"context"
	"errors"

	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/echovisionlab/geul-api/internal/member"
)

const ToolMemberTagList = "member_tag_list"

var memberTagTools = toolRegistry[MemberTagTools]{
	defineTool(oauthTool(ToolMemberTagList, "List Member tags", "List or search administrator-only Member tag IDs, names, and member counts. Use returned IDs to interpret tag_ids from member_admin_get or member_admin_list. Continue with the same query and next_offset. Authorization remains in the owning Member service.", memberTagListInputJSONSchema, memberTagListOutputJSONSchema, true, false), (*MemberTagTools).list),
}

type MemberTagReader interface {
	ListMemberTags(context.Context, member.ListMemberTagsQuery) (member.TagPage, error)
}

type MemberTagTools struct{ reader MemberTagReader }

func NewMemberTagTools(reader MemberTagReader) (*MemberTagTools, error) {
	if interfaceValueIsNil(reader) {
		return nil, errors.New("MCP administrator Member tag reader is required")
	}
	return &MemberTagTools{reader: reader}, nil
}

func (*MemberTagTools) ToolNames() []string { return memberTagTools.names() }

func (*MemberTagTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return memberTagTools.list(), nil
}

func (tools *MemberTagTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	return memberTagTools.call(tools, ctx, name, arguments)
}

func (tools *MemberTagTools) list(ctx context.Context, name string, call toolCall[memberTagListArguments]) (mcpserver.ToolResult, error) {
	arguments := call.Arguments

	if err := rejectNullArguments(arguments, "query", "limit", "offset"); err != nil {
		return executionError(err)
	}
	var input memberTagListArguments
	if err := call.Decode(&input); err != nil {
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
	page, err := tools.reader.ListMemberTags(ctx, member.ListMemberTagsQuery{
		Limit: limit, Offset: input.Offset, Filters: discoverySearchFilters(input.Query),
	})
	if err != nil {
		return expectedToolError(err)
	}
	items := make([]map[string]any, 0, len(page.Tags))
	for _, tag := range page.Tags {
		items = append(items, map[string]any{"id": tag.ID, "name": tag.Name, "member_count": tag.MemberCount})
	}
	pagination := member.PaginationMessage(page.Pagination)
	output := map[string]any{"items": items, "total": pagination.GetTotal(), "limit": pagination.GetLimit(), "offset": pagination.GetOffset(), "has_more": pagination.GetHasMore()}
	if pagination.GetHasMore() {
		output["next_offset"] = int64(pagination.GetOffset()) + int64(pagination.GetLimit())
	}
	return contentResult(output)
}

type memberTagListArguments = struct {
	Query  string `json:"query"`
	Limit  *int32 `json:"limit"`
	Offset int32  `json:"offset"`
}
