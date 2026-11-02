package mcp

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"connectrpc.com/connect"
	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
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
		{"use case", domainerr.Duplicate("member", "nickname", "taken").WithCause(errors.New("private constraint name")), "member with nickname 'taken' already exists"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := fmt.Errorf("private wrapper: %w", tc.err)
			_, err := expectedToolError(original)
			var toolError *mcpserver.ToolExecutionError
			if !errors.As(err, &toolError) || toolError.Message != tc.message {
				t.Fatalf("error=%v, want %q", err, tc.message)
			}
			if toolError.Cause != original { //nolint:errorlint // asserts the original wrapper is kept
				t.Fatal("original cause was dropped")
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

func TestToolErrorsClassifyRequestUUIDFailuresWithoutExposingDatabaseInput(t *testing.T) {
	for _, handler := range []struct {
		name    string
		call    func(error) (mcpserver.ToolResult, error)
		message string
	}{
		{"application", expectedToolError, "request contains an invalid UUID"},
		{"translation", translationCallError, "invalid_argument: request contains an invalid UUID"},
		{"file", fileToolCallError, "invalid_argument: request contains an invalid UUID"},
	} {
		t.Run(handler.name, func(t *testing.T) {
			original := fmt.Errorf("private operation: %w", &pgconn.PgError{
				Code: "22P02", Message: `invalid input syntax for type uuid: "private-request-id"`, Where: "unnamed portal parameter $1",
			})
			_, err := handler.call(original)
			var actionable *mcpserver.ToolExecutionError
			if !errors.As(err, &actionable) || actionable.Message != handler.message {
				t.Fatalf("request error=%v, want %q", err, handler.message)
			}
			if !errors.Is(actionable.Cause, original) {
				t.Fatal("request driver cause was dropped")
			}
			for _, where := range []string{"", `SQL function "stored_uuid" statement 1`} {
				stored := &pgconn.PgError{Code: "22P02", Message: `invalid input syntax for type uuid: "private-stored-id"`, Where: where}
				_, err := handler.call(stored)
				if errors.As(err, &actionable) {
					t.Fatalf("stored data failure became actionable: %v", err)
				}
			}
		})
	}
}
