package mcp

import (
	"encoding/json"

	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
)

func structuredResult(encoded []byte, isError bool) (mcpserver.ToolResult, error) {
	var structured map[string]any
	if err := json.Unmarshal(encoded, &structured); err != nil {
		return mcpserver.ToolResult{}, err
	}
	return mcpserver.ToolResult{
		Content:           []mcpserver.ContentBlock{mcpserver.TextContent(string(encoded))},
		StructuredContent: structured,
		IsError:           isError,
	}, nil
}
