package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"connectrpc.com/connect"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	memberdomain "github.com/echovisionlab/geul-api/internal/member"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestMemberTagToolDescriptor(t *testing.T) {
	_, err := NewMemberTagTools(nil)
	require.Error(t, err)
	var missing *recordingMemberTagReader
	_, err = NewMemberTagTools(missing)
	require.Error(t, err)
	tools, err := NewMemberTagTools(&recordingMemberTagReader{})
	require.NoError(t, err)
	require.Equal(t, []string{ToolMemberTagList}, tools.ToolNames())
	listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assertMCPToolOAuthSecurity(t, listed[0])
	require.Equal(t, true, listed[0].Annotations["readOnlyHint"])
	for _, schema := range []json.RawMessage{listed[0].InputSchema, listed[0].OutputSchema} {
		var object map[string]any
		require.NoError(t, json.Unmarshal(schema, &object))
		require.Equal(t, "object", object["type"])
	}
}

func TestMemberTagListForwardsSupportedSearchAndNativePagination(t *testing.T) {
	for _, test := range []struct {
		arguments string
		limit     int32
		offset    int32
		query     string
		hasMore   bool
	}{
		{arguments: `{}`, limit: 50},
		{arguments: `{"query":"  patrons  ","limit":500,"offset":2147483647}`, limit: 500, offset: 2147483647, query: "patrons", hasMore: true},
	} {
		t.Run(test.arguments, func(t *testing.T) {
			reader := &recordingMemberTagReader{response: &managev1.ListMemberTagsAdminResponse{
				Tags:       []*managev1.MemberTag{{Id: memberAdminTestID, Name: "Patrons", MemberCount: 3}},
				Pagination: &commonv1.PaginationResponse{Total: 7, Limit: test.limit, Offset: test.offset, HasMore: test.hasMore},
			}}
			tools, err := NewMemberTagTools(reader)
			require.NoError(t, err)
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMemberTagList, toolArguments(t, test.arguments))
			require.NoError(t, err)
			require.Equal(t, t.Context(), reader.ctx)
			require.Equal(t, test.limit, reader.request.Pagination.Limit)
			require.Equal(t, test.offset, reader.request.Pagination.Offset)
			require.Equal(t, discoverySearchFilters(test.query), reader.request.Filters)
			require.Empty(t, reader.request.Sorts)
			require.Equal(t, float64(7), result.StructuredContent["total"])
			require.Equal(t, test.hasMore, result.StructuredContent["has_more"])
			if test.hasMore {
				require.Equal(t, float64(int64(test.offset)+int64(test.limit)), result.StructuredContent["next_offset"])
			} else {
				require.NotContains(t, result.StructuredContent, "next_offset")
			}
			item := result.StructuredContent["items"].([]any)[0].(map[string]any)
			require.Equal(t, map[string]any{"id": memberAdminTestID, "name": "Patrons", "member_count": float64(3)}, item)
		})
	}
}

func TestMemberTagListRejectsUnsupportedArgumentsBeforeReading(t *testing.T) {
	for _, arguments := range []string{`{"limit":0}`, `{"limit":501}`, `{"limit":null}`, `{"limit":1.5}`, `{"offset":-1}`, `{"offset":2147483648}`, `{"offset":null}`, `{"query":null}`, `{"type":"private"}`, `{"member_id":"` + memberAdminTestID + `"}`} {
		t.Run(arguments, func(t *testing.T) {
			reader := &recordingMemberTagReader{}
			tools, err := NewMemberTagTools(reader)
			require.NoError(t, err)
			_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMemberTagList, toolArguments(t, arguments))
			var execution *mcpserver.ToolExecutionError
			require.ErrorAs(t, err, &execution)
			require.Zero(t, reader.calls)
		})
	}
}

func TestMemberTagListRetainsNativeAuthenticationAndDenial(t *testing.T) {
	tools, err := NewMemberTagTools(&memberdomain.MemberService{})
	require.NoError(t, err)
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMemberTagList, toolArguments(t, `{}`))
	var execution *mcpserver.ToolExecutionError
	require.ErrorAs(t, err, &execution)
	require.Empty(t, result.Content)
	require.Nil(t, result.StructuredContent)

	reader := &recordingMemberTagReader{err: connect.NewError(connect.CodePermissionDenied, errors.New("Administrator permission required"))}
	tools, err = NewMemberTagTools(reader)
	require.NoError(t, err)
	result, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMemberTagList, toolArguments(t, `{}`))
	require.ErrorAs(t, err, &execution)
	require.Equal(t, "Administrator permission required", execution.Message)
	require.Nil(t, result.StructuredContent)
	_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, "member_tag_unknown", toolArguments(t, `{}`))
	require.ErrorIs(t, err, mcpserver.ErrUnknownTool)
	require.Equal(t, 1, reader.calls)
}

type recordingMemberTagReader struct {
	response *managev1.ListMemberTagsAdminResponse
	request  *managev1.ListMemberTagsAdminRequest
	ctx      context.Context
	err      error
	calls    int
}

func (reader *recordingMemberTagReader) ListMemberTagsAdmin(ctx context.Context, request *connect.Request[managev1.ListMemberTagsAdminRequest]) (*connect.Response[managev1.ListMemberTagsAdminResponse], error) {
	reader.ctx, reader.request = ctx, request.Msg
	reader.calls++
	return connect.NewResponse(reader.response), reader.err
}
