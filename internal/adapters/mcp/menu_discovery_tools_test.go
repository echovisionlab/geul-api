package mcp

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/echovisionlab/geul-api/internal/menu"
	"github.com/echovisionlab/geul-api/internal/sitesettings"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/stretchr/testify/require"
)

func TestMenuLocationsDescriptorAndDependency(t *testing.T) {
	fixture := &recordingMenuDiscovery{}
	var typedNil *recordingMenuDiscovery
	for _, missing := range []MenuLocationReader{nil, typedNil} {
		_, err := NewMenuDiscoveryTools(missing)
		require.Error(t, err)
	}
	tools, err := NewMenuDiscoveryTools(fixture)
	require.NoError(t, err)
	require.Equal(t, []string{ToolMenuLocationsGet}, tools.ToolNames())
	definitions, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	require.NoError(t, err)
	for _, tool := range definitions {
		assertMCPToolOAuthSecurity(t, tool)
		require.Equal(t, true, tool.Annotations["readOnlyHint"])
		require.Equal(t, false, tool.Annotations["destructiveHint"])
	}
	_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMenuLocationsGet, toolArguments(t, `{"key":"private_setting"}`))
	require.Error(t, err)
	require.Empty(t, fixture.keys)
}

func TestMenuLocationsReadsOnlyFixedMenuKeysAndPreservesUnassignedSlots(t *testing.T) {
	fixture := &recordingMenuDiscovery{values: map[string]structured.Value{
		"menu_header_id":          managementWorkID,
		"menu_secondary_id":       nil,
		"menu_footer_id":          managementPostID,
		"menu_avatar_dropdown_id": "",
	}}
	tools, err := NewMenuDiscoveryTools(fixture)
	require.NoError(t, err)
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMenuLocationsGet, toolArguments(t, `{}`))
	require.NoError(t, err)
	require.Equal(t, []string{"menu_header_id", "menu_secondary_id", "menu_footer_id", "menu_avatar_dropdown_id"}, fixture.keys)
	require.Equal(t, map[string]any{"menu_header_id": managementWorkID, "menu_secondary_id": nil, "menu_footer_id": managementPostID, "menu_avatar_dropdown_id": nil}, result.StructuredContent)
	for _, bad := range []structured.Value{true, "not-a-uuid", float64(1)} {
		fixture.values["menu_header_id"] = bad
		_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMenuLocationsGet, toolArguments(t, `{}`))
		require.Error(t, err)
	}
}

func TestMenuDiscoveryReturnsOwnerDenialsAndKeepsInternalErrorsPrivate(t *testing.T) {
	for _, name := range []string{ToolMenuLocationsGet} {
		for _, code := range []connect.Code{connect.CodePermissionDenied, connect.CodeUnauthenticated} {
			fixture := &recordingMenuDiscovery{err: connect.NewError(code, errors.New("native permission denied"))}
			tools, err := NewMenuDiscoveryTools(fixture)
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
		tools, err := NewMenuDiscoveryTools(fixture)
		require.NoError(t, err)
		_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, name, toolArguments(t, `{}`))
		require.ErrorIs(t, err, private)
	}
}

type recordingMenuDiscovery struct {
	page              menu.MenuPage
	paginationRequest menu.ListMenusQuery
	values            map[string]structured.Value
	keys              []string
	actor             *auth.UserInfo
	calls             int
	err               error
}

func (r *recordingMenuDiscovery) ListMenus(ctx context.Context, query menu.ListMenusQuery) (menu.MenuPage, error) {
	r.calls++
	r.actor = auth.GetUser(ctx)
	r.paginationRequest = query
	return r.page, r.err
}

func (r *recordingMenuDiscovery) GetSetting(ctx context.Context, query sitesettings.GetSettingQuery) (sitesettings.Setting, error) {
	r.calls++
	r.actor = auth.GetUser(ctx)
	r.keys = append(r.keys, query.Key)
	return sitesettings.Setting{Key: query.Key, Value: r.values[query.Key]}, r.err
}
