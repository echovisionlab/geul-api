package mcp

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	core "github.com/echovisionlab/geul-api/internal/aidocument"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
)

// expectedToolError turns safe, actionable application outcomes into MCP tool
// errors. Unknown and internal failures remain generic JSON-RPC internal errors
// so implementation details and credentials cannot leak to clients.
func expectedToolError(err error) (mcpserver.ToolResult, error) {
	if err == nil {
		return mcpserver.ToolResult{}, errors.New("MCP tool failed without an error")
	}
	var inputError *core.InputError
	if errors.As(err, &inputError) && inputError != nil {
		return executionError(inputError)
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr == nil {
		if errors.Is(err, context.Canceled) {
			return executionError(errors.New("The request was canceled"))
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return executionError(errors.New("The request timed out; try again"))
		}
		return mcpserver.ToolResult{}, err
	}
	switch connectErr.Code() {
	case connect.CodeInvalidArgument,
		connect.CodeOutOfRange,
		connect.CodeNotFound,
		connect.CodeAlreadyExists,
		connect.CodeFailedPrecondition,
		connect.CodeAborted,
		connect.CodePermissionDenied,
		connect.CodeUnauthenticated,
		connect.CodeResourceExhausted:
		return executionError(errors.New(connectErr.Message()))
	case connect.CodeUnavailable:
		return executionError(errors.New("The service is temporarily unavailable"))
	case connect.CodeCanceled:
		return executionError(errors.New("The request was canceled"))
	case connect.CodeDeadlineExceeded:
		return executionError(errors.New("The request timed out; try again"))
	default:
		return mcpserver.ToolResult{}, err
	}
}

func executionError(err error) (mcpserver.ToolResult, error) {
	return mcpserver.ToolResult{}, &mcpserver.ToolExecutionError{Message: err.Error()}
}
