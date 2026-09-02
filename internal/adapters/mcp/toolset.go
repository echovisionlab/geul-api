package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"

	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
)

// ToolProvider owns a closed group of MCP tool names and their registry and
// dispatch behavior. ToolNames must be stable for the provider lifetime.
type ToolProvider interface {
	mcpserver.ToolRegistry
	mcpserver.ToolDispatcher
	ToolNames() []string
}

// ToolSet composes disjoint domain tool providers without moving their
// application behavior or authorization into the MCP transport.
type ToolSet struct {
	providers []ToolProvider
	byName    map[string]int
}

func NewToolSet(providers ...ToolProvider) (*ToolSet, error) {
	if len(providers) == 0 {
		return nil, errors.New("MCP tool set requires at least one provider")
	}
	set := &ToolSet{
		providers: append([]ToolProvider(nil), providers...),
		byName:    make(map[string]int),
	}
	for index, provider := range set.providers {
		if interfaceValueIsNil(provider) {
			return nil, fmt.Errorf("MCP tool provider %d is required", index)
		}
		names := provider.ToolNames()
		if len(names) == 0 {
			return nil, fmt.Errorf("MCP tool provider %d has no tool names", index)
		}
		for _, name := range names {
			if name == "" {
				return nil, fmt.Errorf("MCP tool provider %d has an empty tool name", index)
			}
			if _, duplicate := set.byName[name]; duplicate {
				return nil, fmt.Errorf("MCP tool name %q is registered more than once", name)
			}
			set.byName[name] = index
		}
	}
	return set, nil
}

func (set *ToolSet) ToolNames() []string {
	names := make([]string, 0, len(set.byName))
	for name := range set.byName {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (set *ToolSet) ListTools(ctx context.Context, principal mcpserver.Principal) ([]mcpserver.Tool, error) {
	listed := make([]mcpserver.Tool, 0, len(set.byName))
	seen := make(map[string]struct{}, len(set.byName))
	for providerIndex, provider := range set.providers {
		tools, err := provider.ListTools(ctx, principal)
		if err != nil {
			return nil, err
		}
		for _, tool := range tools {
			declaredBy, ok := set.byName[tool.Name]
			if !ok || declaredBy != providerIndex {
				return nil, fmt.Errorf("MCP provider returned undeclared tool %q", tool.Name)
			}
			if _, duplicate := seen[tool.Name]; duplicate {
				return nil, fmt.Errorf("MCP provider returned duplicate tool %q", tool.Name)
			}
			seen[tool.Name] = struct{}{}
			listed = append(listed, tool)
		}
	}
	if len(seen) != len(set.byName) {
		return nil, errors.New("MCP provider omitted one or more declared tools")
	}
	return listed, nil
}

func interfaceValueIsNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func cloneToolDefinitions(tools []mcpserver.Tool) []mcpserver.Tool {
	result := make([]mcpserver.Tool, len(tools))
	for index, tool := range tools {
		result[index] = tool
		result[index].InputSchema = append(json.RawMessage(nil), tool.InputSchema...)
		result[index].OutputSchema = append(json.RawMessage(nil), tool.OutputSchema...)
		result[index].SecuritySchemes = cloneSecuritySchemes(tool.SecuritySchemes)
		result[index].Annotations = cloneAnnotations(tool.Annotations)
		result[index].Meta = cloneToolMeta(tool.Meta)
	}
	return result
}

func oauthSecuritySchemes() []mcpserver.ToolSecurityScheme {
	return []mcpserver.ToolSecurityScheme{{Type: "oauth2", Scopes: []string{"mcp", "offline_access"}}}
}

func oauthSecurityMeta() map[string]any {
	return map[string]any{"securitySchemes": oauthSecuritySchemes()}
}

func toolAnnotations(readOnly, destructive, openWorld bool) map[string]any {
	return map[string]any{
		"readOnlyHint":    readOnly,
		"destructiveHint": destructive,
		"openWorldHint":   openWorld,
	}
}

func cloneSecuritySchemes(values []mcpserver.ToolSecurityScheme) []mcpserver.ToolSecurityScheme {
	if values == nil {
		return nil
	}
	result := make([]mcpserver.ToolSecurityScheme, len(values))
	for index, value := range values {
		result[index] = value
		result[index].Scopes = append([]string(nil), value.Scopes...)
	}
	return result
}

func cloneToolMeta(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	result := make(map[string]any, len(value))
	for key, item := range value {
		if key == "securitySchemes" {
			if schemes, ok := item.([]mcpserver.ToolSecurityScheme); ok {
				result[key] = cloneSecuritySchemes(schemes)
				continue
			}
		}
		result[key] = item
	}
	return result
}

func toolDefinitionNames(tools []mcpserver.Tool) []string {
	names := make([]string, len(tools))
	for index, tool := range tools {
		names[index] = tool.Name
	}
	return names
}

func (set *ToolSet) CallTool(
	ctx context.Context,
	principal mcpserver.Principal,
	name string,
	arguments mcpserver.ToolArguments,
) (mcpserver.ToolResult, error) {
	providerIndex, ok := set.byName[name]
	if !ok {
		return mcpserver.ToolResult{}, mcpserver.ErrUnknownTool
	}
	return set.providers[providerIndex].CallTool(ctx, principal, name, arguments)
}

var (
	_ mcpserver.ToolRegistry   = (*ToolSet)(nil)
	_ mcpserver.ToolDispatcher = (*ToolSet)(nil)
)
