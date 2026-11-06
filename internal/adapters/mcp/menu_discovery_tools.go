package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/echovisionlab/geul-api/internal/sitesettings"
)

const (
	ToolMenuLocationsGet = "menu_locations_get"
)

// Menu discovery delegates authorization to the owning Menu and Site Settings
// services. It exposes selection metadata only; DCDP owns reads and mutations.
type MenuLocationReader interface {
	GetSetting(context.Context, sitesettings.GetSettingQuery) (sitesettings.Setting, error)
}

var menuDiscoveryTools = toolRegistry[MenuDiscoveryTools]{
	defineTool(oauthTool(ToolMenuLocationsGet, "Get site menu locations", "Read only the four current site menu assignments through existing administrator Site Settings view authority. Null means no menu is assigned. Pass returned UUIDs as document_id with document_type=menu to document_read/catalog/apply; Menu permissions are checked separately. This tool does not change site settings or return other settings.", menuLocationsInputJSONSchema, menuLocationsOutputJSONSchema, true, false), func(tools *MenuDiscoveryTools, ctx context.Context, name string, call toolCall[struct{}]) (mcpserver.ToolResult, error) {
		if err := call.Decode(&struct{}{}); err != nil {
			return executionError(err)
		}
		output := make(map[string]any, 4)
		for _, key := range []string{"menu_header_id", "menu_secondary_id", "menu_footer_id", "menu_avatar_dropdown_id"} {
			setting, err := tools.settings.GetSetting(ctx, sitesettings.GetSettingQuery{Key: key})
			if err != nil {
				return expectedToolError(err)
			}
			if setting.Key != key {
				return mcpserver.ToolResult{}, fmt.Errorf("Site Settings returned an invalid menu assignment for %s", key)
			}
			switch value := setting.Value.(type) {
			case nil:
				output[key] = nil
			case string:
				if value == "" {
					output[key] = nil
				} else {
					if err := validateUUID(key, value); err != nil {
						return mcpserver.ToolResult{}, fmt.Errorf("invalid stored menu assignment: %w", err)
					}
					output[key] = value
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
	}),
}

type MenuDiscoveryTools struct {
	settings MenuLocationReader
}

func NewMenuDiscoveryTools(settings MenuLocationReader) (*MenuDiscoveryTools, error) {
	if interfaceValueIsNil(settings) {
		return nil, errors.New("MCP Site Settings reader is required")
	}
	return &MenuDiscoveryTools{settings: settings}, nil
}

func (*MenuDiscoveryTools) ToolNames() []string { return menuDiscoveryTools.names() }
func (*MenuDiscoveryTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return menuDiscoveryTools.list(), nil
}

func (tools *MenuDiscoveryTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	return menuDiscoveryTools.call(tools, ctx, name, arguments)
}
