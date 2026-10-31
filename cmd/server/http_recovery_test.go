package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authentication"
	"github.com/echovisionlab/geul-api/internal/config"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/telemetry"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// syncBuffer collects JSON log lines written from server goroutines.
type syncBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *syncBuffer) records(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(b.buffer.String()), "\n") {
		if line == "" {
			continue
		}
		record := map[string]any{}
		require.NoError(t, json.Unmarshal([]byte(line), &record), line)
		records = append(records, record)
	}
	return records
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func captureServerLogs(t *testing.T) *syncBuffer {
	t.Helper()
	output := &syncBuffer{}
	previous := slog.Default()
	// Same handler stack as configureBootstrapLogging.
	slog.SetDefault(slog.New(telemetry.NewNormalizingHandler(slog.NewJSONHandler(output, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return output
}

// findLogRecord matches a record by message or by its event attribute.
func findLogRecord(records []map[string]any, name string) map[string]any {
	for _, record := range records {
		if record["msg"] == name || record["event"] == name {
			return record
		}
	}
	return nil
}

type discardSecurityAccess struct{}

func (discardSecurityAccess) AppendSecurityAccess(context.Context, sharedtelemetry.SecurityAccessRecord) error {
	return nil
}

const recoveryTestProcedure = "/geul.open.v1.NewsletterService/Unsubscribe"

const recoveryPrivateToken = "newsletter-token-private-value"

// serveConnectThroughProductionChain mounts one unary RPC behind the same
// ingress handler and shared interceptor order the server registers.
func serveConnectThroughProductionChain(
	t *testing.T,
	handle func(context.Context) error,
) *connect.Client[openv1.UnsubscribeNewsletterRequest, openv1.UnsubscribeNewsletterResponse] {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(recoveryTestProcedure, connect.NewUnaryHandler(
		recoveryTestProcedure,
		func(ctx context.Context, _ *connect.Request[openv1.UnsubscribeNewsletterRequest]) (*connect.Response[openv1.UnsubscribeNewsletterResponse], error) {
			if err := handle(ctx); err != nil {
				return nil, err
			}
			return connect.NewResponse(&openv1.UnsubscribeNewsletterResponse{Success: true}), nil
		},
		connectInterceptors(telemetry.NewTracingInterceptor(), telemetry.NewAccessLogInterceptor(discardSecurityAccess{})),
	))
	server := httptest.NewServer(newApplicationHTTPServer(mux, &config.Config{}).Handler)
	t.Cleanup(server.Close)
	return connect.NewClient[openv1.UnsubscribeNewsletterRequest, openv1.UnsubscribeNewsletterResponse](
		server.Client(), server.URL+recoveryTestProcedure,
	)
}

func requireOpaqueInternalResponse(t *testing.T, err error) {
	t.Helper()
	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	require.Equal(t, connect.CodeInternal, connectErr.Code())
	require.Equal(t, errs.MsgInternal, connectErr.Message())
	require.Empty(t, connectErr.Details())
}

// requireSingleTerminalRecord asserts that a failed RPC produced exactly one
// Error record: request.completed, carrying the real cause.
func requireSingleTerminalRecord(t *testing.T, logs *syncBuffer) map[string]any {
	t.Helper()
	records := logs.records(t)
	require.Len(t, records, 1, logs.String())
	completed := records[0]
	require.Equal(t, "request.completed", completed["event"], logs.String())
	require.Equal(t, "ERROR", completed["level"])
	require.EqualValues(t, http.StatusInternalServerError, completed["status_code"])
	require.NotEmpty(t, completed["request_id"])
	return completed
}

func TestConnectRoutesHideInternalCausesFromClientsAndRequestLog(t *testing.T) {
	logs := captureServerLogs(t)
	client := serveConnectThroughProductionChain(t, func(context.Context) error {
		return errs.Internal(fmt.Errorf("load subscription for %s: %w", recoveryPrivateToken,
			errors.New(`ERROR: column "secret_digest" does not exist (SQLSTATE 42703)`)))
	})

	_, err := client.CallUnary(t.Context(), connect.NewRequest(&openv1.UnsubscribeNewsletterRequest{Token: recoveryPrivateToken}))

	requireOpaqueInternalResponse(t, err)
	require.NotContains(t, err.Error(), "secret_digest")
	completed := requireSingleTerminalRecord(t, logs)
	require.Equal(t, `internal: load subscription for [redacted]: ERROR: column "secret_digest" does not exist (SQLSTATE 42703)`,
		completed["failure_cause"])
	require.NotContains(t, logs.String(), recoveryPrivateToken)
}

// A Kratos failure deep in a handler leaves one record: request.completed with
// the upstream body redacted and the identifiers the error carried.
func TestConnectRoutesRecordKratosFailureOnceWithIdentifiers(t *testing.T) {
	logs := captureServerLogs(t)
	const upstreamBody = `{"error":{"reason":"traits.nickname quiet-river conflicts"}}`
	client := serveConnectThroughProductionChain(t, func(context.Context) error {
		lockErr := fmt.Errorf("lock identity state: %w", &auth.KratosHTTPError{StatusCode: http.StatusConflict, Body: upstreamBody})
		return errs.Internal(failurecause.WithAttrs(
			failurecause.WithPrivate(errors.Join(lockErr, errors.New("reserve nickname quiet-river")), "quiet-river"),
			slog.String("entity_type", "member"), slog.String("entity_id", "0b9c1f9e-5a8b-4c1e-9d7a-3f2e1d0c9b8a"),
		))
	})

	_, err := client.CallUnary(t.Context(), connect.NewRequest(&openv1.UnsubscribeNewsletterRequest{Token: recoveryPrivateToken}))

	requireOpaqueInternalResponse(t, err)
	completed := requireSingleTerminalRecord(t, logs)
	require.Equal(t, "internal: lock identity state: kratos returned 409: [redacted]\nreserve nickname [redacted]", completed["failure_cause"])
	require.Equal(t, "member", completed["entity_type"])
	require.Equal(t, "0b9c1f9e-5a8b-4c1e-9d7a-3f2e1d0c9b8a", completed["entity_id"])
	require.NotContains(t, logs.String(), "quiet-river")
	require.NotContains(t, logs.String(), "traits.nickname")
}

func TestConnectRoutesRecoverHandlerPanics(t *testing.T) {
	logs := captureServerLogs(t)
	client := serveConnectThroughProductionChain(t, func(context.Context) error {
		panic(errors.New("unsubscribe store is not configured"))
	})

	_, err := client.CallUnary(t.Context(), connect.NewRequest(&openv1.UnsubscribeNewsletterRequest{Token: recoveryPrivateToken}))

	requireOpaqueInternalResponse(t, err)
	completed := requireSingleTerminalRecord(t, logs)
	require.Equal(t, "unsubscribe store is not configured", completed["failure_cause"])
	require.Contains(t, completed["panic_stack"], "TestConnectRoutesRecoverHandlerPanics")
}

func TestApplicationHTTPServerRecoversPlainHandlerPanics(t *testing.T) {
	logs := captureServerLogs(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /upload/status", func(http.ResponseWriter, *http.Request) {
		panic("upload status index out of range")
	})
	handler := newApplicationHTTPServer(mux, &config.Config{}).Handler

	response := httptest.NewRecorder()
	require.NotPanics(t, func() {
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "https://api.test/upload/status", nil))
	})

	require.Equal(t, http.StatusInternalServerError, response.Code)
	require.NotContains(t, response.Body.String(), "index out of range")
	panicked := findLogRecord(logs.records(t), "HTTP handler panicked")
	require.NotNil(t, panicked, logs.String())
	require.Equal(t, "ERROR", panicked["level"])
	require.Equal(t, "upload status index out of range", panicked["failure_cause"])
	require.Contains(t, panicked["panic_stack"], "TestApplicationHTTPServerRecoversPlainHandlerPanics")
	require.NotEmpty(t, panicked["request_id"])
	require.Equal(t, panicked["request_id"], response.Header().Get(telemetry.RequestIDHeader))
}

func TestRecoverHTTPPanicsAbortsStartedResponsesAndKeepsAbortHandler(t *testing.T) {
	captureServerLogs(t)
	for name, handler := range map[string]http.HandlerFunc{
		"started response": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			panic("late failure")
		},
		"abort handler": func(http.ResponseWriter, *http.Request) {
			panic(http.ErrAbortHandler)
		},
	} {
		t.Run(name, func(t *testing.T) {
			require.PanicsWithValue(t, http.ErrAbortHandler, func() {
				recoverHTTPPanics(handler).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
			})
		})
	}
}

func TestPrivateMCPServerRecoversHandlerPanics(t *testing.T) {
	captureServerLogs(t)
	server, err := newMCPPrivateHTTPServer(mcpPrivateHandlers{
		authorAdmission: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("admission broke") }),
		oathkeeperPAT:   http.NotFoundHandler(),
	}, &config.Config{})
	require.NoError(t, err)

	response := httptest.NewRecorder()
	require.NotPanics(t, func() {
		server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "http://private.test"+authentication.MCPGatewayAuthorAdmissionPath, nil))
	})
	require.Equal(t, http.StatusInternalServerError, response.Code)
}
