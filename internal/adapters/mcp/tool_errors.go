package mcp

import (
	"errors"

	"connectrpc.com/connect"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
)

// expectedToolError turns safe, actionable application outcomes into MCP tool
// errors. Unknown and internal failures remain generic JSON-RPC internal errors
// so implementation details and credentials cannot leak to clients.
func expectedToolError(err error) (mcpserver.ToolResult, error) {
	if err == nil {
		return mcpserver.ToolResult{}, errors.New("MCP tool failed without an error")
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr == nil {
		return mcpserver.ToolResult{}, err
	}
	switch connectErr.Code() {
	case connect.CodeInvalidArgument,
		connect.CodeNotFound,
		connect.CodeAlreadyExists,
		connect.CodeFailedPrecondition,
		connect.CodePermissionDenied,
		connect.CodeUnauthenticated,
		connect.CodeResourceExhausted:
		return executionError(errors.New(connectErr.Message()))
	case connect.CodeUnavailable:
		return executionError(errors.New("Geul service is temporarily unavailable"))
	default:
		return mcpserver.ToolResult{}, err
	}
}
