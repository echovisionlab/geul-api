package mcp

import (
	"context"
	"fmt"
	"reflect"

	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
)

type registeredTool[Owner any] struct {
	definition mcpserver.Tool
	call       func(*Owner, context.Context, string, mcpserver.ToolArguments) (mcpserver.ToolResult, error)
}

type toolRegistry[Owner any] []registeredTool[Owner]

func (registry toolRegistry[Owner]) names() []string {
	names := make([]string, 0, len(registry))
	for _, entry := range registry {
		names = append(names, entry.definition.Name)
	}
	return names
}

func (registry toolRegistry[Owner]) list() []mcpserver.Tool {
	definitions := make([]mcpserver.Tool, 0, len(registry))
	for _, entry := range registry {
		definitions = append(definitions, entry.definition)
	}
	return cloneToolDefinitions(definitions)
}

func (registry toolRegistry[Owner]) call(owner *Owner, ctx context.Context, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	for _, entry := range registry {
		if entry.definition.Name == name {
			return entry.call(owner, ctx, name, arguments)
		}
	}
	return mcpserver.ToolResult{}, mcpserver.ErrUnknownTool
}

type toolCall[Input any] struct {
	Arguments mcpserver.ToolArguments
}

// Decode remains lazy so each tool keeps its validation and authorization order.
func (call toolCall[Input]) Decode(input *Input) error {
	return decodeArguments(call.Arguments, input)
}

func defineTool[Owner, Input any](definition mcpserver.Tool, handler func(*Owner, context.Context, string, toolCall[Input]) (mcpserver.ToolResult, error)) registeredTool[Owner] {
	schema, err := generateInputSchema(definition.InputSchema, reflect.TypeFor[Input]())
	if err != nil {
		panic(fmt.Sprintf("MCP tool %s input schema: %v", definition.Name, err))
	}
	definition.InputSchema = schema
	return registeredTool[Owner]{definition: definition, call: func(owner *Owner, ctx context.Context, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
		return handler(owner, ctx, name, toolCall[Input]{Arguments: arguments})
	}}
}
