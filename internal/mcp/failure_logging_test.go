package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/stretchr/testify/require"
)

func TestMCPLogsOriginalCauseBeforeGenericResponse(t *testing.T) {
	for _, expected := range []bool{false, true} {
		t.Run(fmt.Sprint(expected), func(t *testing.T) {
			var output bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(telemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
			t.Cleanup(func() { slog.SetDefault(previous) })
			cause := errors.New(`database unavailable: Private Person nested-secret reader@example.test token=private-token https://provider.test/private <blockquote> {constraint detail} column "title" profile article <img>`)
			handler := newTestHandler(t, testDependencies{dispatcher: dispatcherFunc(func(context.Context, Principal, string, ToolArguments) (ToolResult, error) {
				if expected {
					return ToolResult{}, &ToolExecutionError{Message: "Service temporarily unavailable", Cause: connect.NewError(connect.CodeUnavailable, cause)}
				}
				return ToolResult{}, cause
			})})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, rpcRequest(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"document_read","arguments":{"name":"Private Person","nested":{"password":"nested-secret"},"profile":"article","tag_name":"<img>"}}}`))
			var entry map[string]any
			require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
			require.Equal(t, "ERROR", entry["level"])
			require.Equal(t, "document_read", entry["tool"])
			require.Contains(t, entry["failure_cause"], "database unavailable")
			for _, private := range []string{"Private Person", "nested-secret", "reader@example.test", "private-token"} {
				require.NotContains(t, output.String(), private)
			}
			require.Contains(t, entry["failure_cause"], "https://provider.test/private <blockquote> {constraint detail}")
			require.Contains(t, entry["failure_cause"], `column "title" profile article <img>`)
			require.NotContains(t, response.Body.String(), "database unavailable")
			if expected {
				require.Contains(t, response.Body.String(), `"isError":true`)
			} else {
				require.Contains(t, response.Body.String(), `"code":-32603`)
			}
		})
	}
}

// Tool calls reach services without the RPC boundary; the classified operation
// context must still reach the log.
func TestMCPLogsClassifiedOperationContext(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(telemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	cause := errs.Internal(fmt.Errorf("load campaign: %w", context.DeadlineExceeded))
	handler := newTestHandler(t, testDependencies{dispatcher: dispatcherFunc(func(context.Context, Principal, string, ToolArguments) (ToolResult, error) {
		return ToolResult{}, &ToolExecutionError{Message: "Service temporarily unavailable", Cause: cause}
	})})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, rpcRequest(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"document_read","arguments":{}}}`))

	var entry map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
	require.Equal(t, "load campaign: context deadline exceeded", entry["failure_cause"])
	require.NotContains(t, response.Body.String(), "load campaign")
}
