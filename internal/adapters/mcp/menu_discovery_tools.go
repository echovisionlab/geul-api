package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/echovisionlab/geul-api/internal/menu"
	"github.com/echovisionlab/geul-api/internal/sitesettings"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

const (
	ToolMenuList         = "menu_list"
	ToolMenuLocationsGet = "menu_locations_get"
)

// Menu discovery delegates authorization to the owning Menu and Site Settings
// services. It exposes selection metadata only; DCDP owns reads and mutations.
type MenuDiscovery interface {
	ListMenus(context.Context, menu.ListMenusQuery) (menu.MenuPage, error)
}

type MenuLocationReader interface {
	GetSetting(context.Context, sitesettings.GetSettingQuery) (*managev1.SiteSetting, error)
}

var menuDiscoveryTools = []mcpserver.Tool{
	oauthTool(ToolMenuList, "List menus", "List canonical Menu UUIDs, names, and source locales through the existing authorized management list. Use menu_locations_get to identify the site's header, secondary, footer, and avatar menus; menu names do not determine their placement. Pass id unchanged as d with p=menu to document_open/read/catalog/apply. Read current revisions and stable item handles before editing. Continue with next_offset. Menu contents are omitted.", menuListInputJSONSchema, menuListOutputJSONSchema, true, false),
	oauthTool(ToolMenuLocationsGet, "Get site menu locations", "Read only the four current site menu assignments through existing administrator Site Settings view authority. Null means no menu is assigned. Pass returned UUIDs as d with p=menu to document_open/read/catalog/apply; Menu permissions are checked separately. This tool does not change site settings or return other settings.", menuLocationsInputJSONSchema, menuLocationsOutputJSONSchema, true, false),
}

type MenuDiscoveryTools struct {
	menus    MenuDiscovery
	settings MenuLocationReader
}

func NewMenuDiscoveryTools(menus MenuDiscovery, settings MenuLocationReader) (*MenuDiscoveryTools, error) {
	if interfaceValueIsNil(menus) || interfaceValueIsNil(settings) {
		return nil, errors.New("MCP Menu discovery and Site Settings reader are required")
	}
	return &MenuDiscoveryTools{menus: menus, settings: settings}, nil
}

func (*MenuDiscoveryTools) ToolNames() []string { return toolDefinitionNames(menuDiscoveryTools) }
func (*MenuDiscoveryTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return cloneToolDefinitions(menuDiscoveryTools), nil
}

func (tools *MenuDiscoveryTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	switch name {
	case ToolMenuList:
		if err := rejectNullArguments(arguments, "limit", "offset"); err != nil {
			return executionError(err)
		}
		var input struct {
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
		page, err := tools.menus.ListMenus(ctx, menu.ListMenusQuery{Limit: limit, Offset: input.Offset})
		if err != nil {
			return expectedToolError(err)
		}
		items := make([]map[string]any, 0, len(page.Menus))
		for _, listed := range page.Menus {
			items = append(items, map[string]any{"id": listed.ID, "name": listed.Name, "source_locale": listed.SourceLocale})
		}
		output := map[string]any{"items": items, "total": page.Total, "limit": limit, "offset": input.Offset, "has_more": page.HasMore}
		if page.HasMore {
			output["next_offset"] = int64(input.Offset) + int64(limit)
		}
		return contentResult(output)
	case ToolMenuLocationsGet:
		if err := decodeArguments(arguments, &struct{}{}); err != nil {
			return executionError(err)
		}
		output := make(map[string]any, 4)
		for _, key := range []string{"menu_header_id", "menu_secondary_id", "menu_footer_id", "menu_avatar_dropdown_id"} {
			setting, err := tools.settings.GetSetting(ctx, sitesettings.GetSettingQuery{Key: key})
			if err != nil {
				return expectedToolError(err)
			}
			if setting == nil || setting.Key != key || setting.Value == nil {
				return mcpserver.ToolResult{}, fmt.Errorf("Site Settings returned an invalid menu assignment for %s", key)
			}
			switch value := setting.Value.Kind.(type) {
			case *structpb.Value_NullValue:
				output[key] = nil
			case *structpb.Value_StringValue:
				if value.StringValue == "" {
					output[key] = nil
				} else {
					if err := validateUUID(key, value.StringValue); err != nil {
						return mcpserver.ToolResult{}, fmt.Errorf("invalid stored menu assignment: %w", err)
					}
					output[key] = value.StringValue
				}
			default:
				return mcpserver.ToolResult{}, fmt.Errorf("Site Settings returned a non-text menu assignment for %s", key)
			}
		}
		encoded, err := json.Marshal(output)
		if err != nil {
			return mcpserver.ToolResult{}, fmt.Errorf("encode menu assignments: %w", err)
		}
		return structuredResult(encoded, false)
	default:
		return mcpserver.ToolResult{}, mcpserver.ErrUnknownTool
	}
}
