package mcp

import (
	"connectrpc.com/connect"
	"context"
	"errors"
	"fmt"
	core "github.com/echovisionlab/geul-api/internal/aidocument"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"testing"
)

func TestExpectedToolErrorPreservesActionableFailuresWithoutPrivateContext(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		message string
	}{
		{"document input", &core.InputError{Message: "field read requires field selectors only"}, "field read requires field selectors only"},
		{"out of range", connect.NewError(connect.CodeOutOfRange, errors.New("page size exceeds maximum")), "page size exceeds maximum"},
		{"canceled", context.Canceled, "The request was canceled"},
		{"deadline", context.DeadlineExceeded, "The request timed out; try again"},
		{"connect canceled", connect.NewError(connect.CodeCanceled, errors.New("private upstream context")), "The request was canceled"},
		{"connect deadline", connect.NewError(connect.CodeDeadlineExceeded, errors.New("private upstream context")), "The request timed out; try again"},
		{"unavailable", connect.NewError(connect.CodeUnavailable, errors.New("private upstream context")), "The service is temporarily unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := expectedToolError(fmt.Errorf("private wrapper: %w", tc.err))
			var toolError *mcpserver.ToolExecutionError
			if !errors.As(err, &toolError) || toolError.Message != tc.message {
				t.Fatalf("error=%v, want %q", err, tc.message)
			}
		})
	}
	for _, err := range []error{errors.New("private server failure"), connect.NewError(connect.CodeInternal, errors.New("private database details")), (*core.InputError)(nil)} {
		_, got := expectedToolError(err)
		var actionable *mcpserver.ToolExecutionError
		if errors.As(got, &actionable) {
			t.Fatalf("internal failure was exposed as actionable: %v", got)
		}
	}
}
