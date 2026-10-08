package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestMenuDiscoveryDescriptorsAndDependencies(t *testing.T) {
	fixture := &recordingMenuDiscovery{}
	var typedNil *recordingMenuDiscovery
	for _, missing := range []struct {
		menus    MenuDiscovery
		settings MenuLocationReader
	}{
		{nil, fixture}, {fixture, nil}, {typedNil, fixture}, {fixture, typedNil},
	} {
		_, err := NewMenuDiscoveryTools(missing.menus, missing.settings)
		require.Error(t, err)
	}
	tools, err := NewMenuDiscoveryTools(fixture, fixture)
	require.NoError(t, err)
	require.Equal(t, []string{ToolMenuList, ToolMenuLocationsGet}, tools.ToolNames())
	definitions, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	require.NoError(t, err)
	for _, tool := range definitions {
		assertMCPToolOAuthSecurity(t, tool)
		require.Equal(t, true, tool.Annotations["readOnlyHint"])
		require.Equal(t, false, tool.Annotations["destructiveHint"])
		for _, raw := range []json.RawMessage{tool.InputSchema, tool.OutputSchema} {
			var schema map[string]any
			require.NoError(t, json.Unmarshal(raw, &schema))
			require.Equal(t, false, schema["additionalProperties"])
		}
	}
}

func TestMenuDiscoveryPreservesNativeAuthorityAndProjectsSelectionOnly(t *testing.T) {
	actor := &auth.UserInfo{Authenticated: true, IdentityID: "identity", MemberID: "member"}
	ctx := auth.WithUser(t.Context(), actor)
	fixture := &recordingMenuDiscovery{
		menus:      []*managev1.Menu{nil, {Id: managementWorkID, Name: "Main", SourceLocale: "ko", Items: []*managev1.MenuItem{{Label: "private menu body"}}}},
		pagination: &commonv1.PaginationResponse{Total: 31, HasMore: true},
	}
	tools, err := NewMenuDiscoveryTools(fixture, fixture)
	require.NoError(t, err)
	result, err := tools.CallTool(ctx, mcpserver.Principal{}, ToolMenuList, toolArguments(t, `{"limit":10,"offset":20}`))
	require.NoError(t, err)
	require.Same(t, actor, fixture.actor)
	require.Equal(t, int32(10), fixture.paginationRequest.Limit)
	require.Equal(t, int32(20), fixture.paginationRequest.Offset)
	require.Equal(t, float64(30), result.StructuredContent["next_offset"])
	items := result.StructuredContent["items"].([]any)
	require.Equal(t, []any{map[string]any{"id": managementWorkID, "name": "Main", "source_locale": "ko"}}, items)
	require.NotContains(t, result.Content[0]["text"], "private menu body")
	fixture.pagination = nil
	result, err = tools.CallTool(ctx, mcpserver.Principal{}, ToolMenuList, toolArguments(t, `{}`))
	require.NoError(t, err)
	require.Equal(t, int32(20), fixture.paginationRequest.Limit)
	require.NotContains(t, result.StructuredContent, "next_offset")
}

func TestMenuDiscoveryRejectsInvalidInputsBeforeCallingOwners(t *testing.T) {
	for _, input := range []string{`{"limit":0}`, `{"limit":101}`, `{"limit":null}`, `{"limit":1.5}`, `{"offset":-1}`, `{"offset":2147483648}`, `{"offset":null}`, `{"query":"Main"}`} {
		fixture := &recordingMenuDiscovery{}
		tools, err := NewMenuDiscoveryTools(fixture, fixture)
		require.NoError(t, err)
		_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMenuList, toolArguments(t, input))
		var executionError *mcpserver.ToolExecutionError
		require.ErrorAs(t, err, &executionError, input)
		require.Zero(t, fixture.calls, input)
	}
	fixture := &recordingMenuDiscovery{pagination: &commonv1.PaginationResponse{HasMore: true}}
	tools, err := NewMenuDiscoveryTools(fixture, fixture)
	require.NoError(t, err)
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMenuList, toolArguments(t, `{"offset":2147483640,"limit":20}`))
	require.NoError(t, err)
	require.Equal(t, float64(2147483660), result.StructuredContent["next_offset"])
	_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMenuLocationsGet, toolArguments(t, `{"key":"private_setting"}`))
	require.Error(t, err)
	require.Empty(t, fixture.keys)
}

func TestMenuLocationsReadsOnlyFixedMenuKeysAndPreservesUnassignedSlots(t *testing.T) {
	fixture := &recordingMenuDiscovery{values: map[string]*structpb.Value{
		"menu_header_id":          structpb.NewStringValue(managementWorkID),
		"menu_secondary_id":       structpb.NewNullValue(),
		"menu_footer_id":          structpb.NewStringValue(managementPostID),
		"menu_avatar_dropdown_id": structpb.NewStringValue(""),
	}}
	tools, err := NewMenuDiscoveryTools(fixture, fixture)
	require.NoError(t, err)
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMenuLocationsGet, toolArguments(t, `{}`))
	require.NoError(t, err)
	require.Equal(t, []string{"menu_header_id", "menu_secondary_id", "menu_footer_id", "menu_avatar_dropdown_id"}, fixture.keys)
	require.Equal(t, map[string]any{"menu_header_id": managementWorkID, "menu_secondary_id": nil, "menu_footer_id": managementPostID, "menu_avatar_dropdown_id": nil}, result.StructuredContent)
	for _, bad := range []*structpb.Value{structpb.NewBoolValue(true), structpb.NewStringValue("not-a-uuid"), nil} {
		fixture.values["menu_header_id"] = bad
		_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMenuLocationsGet, toolArguments(t, `{}`))
		require.Error(t, err)
	}
}

func TestMenuDiscoveryReturnsOwnerDenialsAndKeepsInternalErrorsPrivate(t *testing.T) {
	for _, name := range []string{ToolMenuList, ToolMenuLocationsGet} {
		for _, code := range []connect.Code{connect.CodePermissionDenied, connect.CodeUnauthenticated} {
			fixture := &recordingMenuDiscovery{err: connect.NewError(code, errors.New("native permission denied"))}
			tools, err := NewMenuDiscoveryTools(fixture, fixture)
			require.NoError(t, err)
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, name, toolArguments(t, `{}`))
			var executionError *mcpserver.ToolExecutionError
			require.ErrorAs(t, err, &executionError)
			require.Equal(t, "native permission denied", executionError.Message)
			require.Nil(t, result.StructuredContent)
			require.Equal(t, 1, fixture.calls)
		}
		private := connect.NewError(connect.CodeInternal, errors.New("private failure"))
		fixture := &recordingMenuDiscovery{err: private}
		tools, err := NewMenuDiscoveryTools(fixture, fixture)
		require.NoError(t, err)
		_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, name, toolArguments(t, `{}`))
		require.ErrorIs(t, err, private)
	}
}

type recordingMenuDiscovery struct {
	menus             []*managev1.Menu
	pagination        *commonv1.PaginationResponse
	paginationRequest *commonv1.PaginationRequest
	values            map[string]*structpb.Value
	keys              []string
	actor             *auth.UserInfo
	calls             int
	err               error
}

func (r *recordingMenuDiscovery) ListMenus(ctx context.Context, req *connect.Request[managev1.ListMenusRequest]) (*connect.Response[managev1.ListMenusResponse], error) {
	r.calls++
	r.actor = auth.GetUser(ctx)
	r.paginationRequest = req.Msg.Pagination
	return connect.NewResponse(&managev1.ListMenusResponse{Menus: r.menus, Pagination: r.pagination}), r.err
}

func (r *recordingMenuDiscovery) GetSetting(ctx context.Context, req *connect.Request[managev1.GetSettingRequest]) (*connect.Response[managev1.GetSettingResponse], error) {
	r.calls++
	r.actor = auth.GetUser(ctx)
	r.keys = append(r.keys, req.Msg.Key)
	return connect.NewResponse(&managev1.GetSettingResponse{Setting: &managev1.SiteSetting{Key: req.Msg.Key, Value: r.values[req.Msg.Key]}}), r.err
}
