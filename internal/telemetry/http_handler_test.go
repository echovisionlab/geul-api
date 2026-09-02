package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/structured"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/types/known/emptypb"
)

type securityAccessWriterStub struct {
	records []sharedtelemetry.SecurityAccessRecord
	err     error
}

func (writer *securityAccessWriterStub) AppendSecurityAccess(_ context.Context, record sharedtelemetry.SecurityAccessRecord) error {
	writer.records = append(writer.records, record)
	return writer.err
}

func TestHTTPHandlerReplacesRequestIDAndKeepsRawURLOutOfTelemetry(t *testing.T) {
	const (
		attackerRequestID = "attacker-controlled"
		sensitivePath     = "private-member-id"
		sensitiveQuery    = "person@example.com"
	)
	recorder, restore := installHTTPTestTelemetry(t)
	defer restore()
	logBuffer, restoreLogger := captureDefaultLogs()
	defer restoreLogger()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /members/{id}", func(w http.ResponseWriter, r *http.Request) {
		requestContext, ok := RequestContextFrom(r.Context())
		require.True(t, ok)
		require.NotEqual(t, attackerRequestID, requestContext.RequestID)
		_ = WithActor(r.Context(), MemberActor{MemberID: "member-1", IdentityID: "identity-1"})
		w.WriteHeader(http.StatusNoContent)
	})

	request := httptest.NewRequest(
		http.MethodGet,
		"/members/"+sensitivePath+"?email="+sensitiveQuery,
		nil,
	)
	request.Header.Set(RequestIDHeader, attackerRequestID)
	response := httptest.NewRecorder()
	NewHTTPHandler(mux, nil).ServeHTTP(response, request)
	requestID := response.Header().Get(RequestIDHeader)
	require.NotEqual(t, attackerRequestID, requestID)
	_, err := uuid.Parse(requestID)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, response.Code)

	entry := decodeSingleLogEntry(t, logBuffer.Bytes())
	require.Equal(t, "request.completed", entry["event"])
	require.Equal(t, requestID, entry["request_id"])
	require.Equal(t, "member", entry["actor_kind"])
	require.Equal(t, "member-1", entry["actor_member_id"])
	require.Equal(t, "/members/{id}", entry["http_route"])
	require.NotContains(t, logBuffer.String(), sensitivePath)
	require.NotContains(t, logBuffer.String(), sensitiveQuery)

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	require.Equal(t, "GET /members/{id}", spans[0].Name())
	assertSpansExclude(t, spans, sensitivePath, sensitiveQuery, attackerRequestID)
}

func TestConnectRequestEmitsOneTerminalRecordAndContinuesIncomingTrace(t *testing.T) {
	const sensitiveError = "person@example.com: forbidden provider response"
	recorder, restore := installHTTPTestTelemetry(t)
	defer restore()
	logBuffer, restoreLogger := captureDefaultLogs()
	defer restoreLogger()

	securityWriter := &securityAccessWriterStub{err: errors.New("audit storage unavailable")}
	access := NewAccessLogInterceptor(securityWriter)
	tracingInterceptor := NewTracingInterceptor()
	handler := connect.NewUnaryHandler(
		"/test.v1.TestService/GetThing",
		func(ctx context.Context, _ *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
			_ = WithActor(ctx, MemberActor{MemberID: "member-2", IdentityID: "identity-2"})
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New(sensitiveError))
		},
		connect.WithInterceptors(tracingInterceptor, access),
	)
	mux := http.NewServeMux()
	mux.Handle("/test.v1.TestService/", handler)

	traceID, err := trace.TraceIDFromHex("11111111111111111111111111111111")
	require.NoError(t, err)
	parentSpanID, err := trace.SpanIDFromHex("2222222222222222")
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/test.v1.TestService/GetThing", strings.NewReader("{}"))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Traceparent", "00-"+traceID.String()+"-"+parentSpanID.String()+"-01")
	response := httptest.NewRecorder()
	NewHTTPHandler(mux, nil).ServeHTTP(response, request)
	requestID := response.Header().Get(RequestIDHeader)
	require.Equal(t, http.StatusForbidden, response.Code)

	entries := decodeLogEntries(t, logBuffer.Bytes())
	require.Len(t, entries, 1)
	entry := entries[0]
	require.Equal(t, "request.completed", entry["event"])
	require.Equal(t, "blocked", entry["outcome"])
	require.Equal(t, float64(http.StatusForbidden), entry["status_code"])
	require.Equal(t, "permission_denied", entry["error_code"])
	require.Equal(t, "test.v1.TestService", entry["rpc_service"])
	require.Equal(t, "GetThing", entry["rpc_method"])
	require.Equal(t, "member-2", entry["actor_member_id"])
	require.NotContains(t, logBuffer.String(), sensitiveError)
	require.Len(t, securityWriter.records, 1)
	denial := securityWriter.records[0]
	require.Equal(t, sharedtelemetry.SecurityAuthorizationDenied, denial.Action)
	require.Equal(t, "/test.v1.TestService/GetThing", denial.AttemptedAction)
	require.Equal(t, sharedtelemetry.AuthorizationProcedureInvokePermission, denial.Permission)
	require.Equal(t, string(sharedtelemetry.AuthorizationDeniedPermissionDenied), denial.Reason)
	require.Equal(t, "member-2", denial.MemberID)
	require.Equal(t, requestID, denial.RequestID)
	require.Equal(t, "192.0.2.1", denial.SourceIP)

	spans := recorder.Ended()
	require.Len(t, spans, 2)
	for _, span := range spans {
		require.Equal(t, traceID, span.SpanContext().TraceID())
	}
	assertSpansExclude(t, spans, sensitiveError)
}

func TestHTTPHandlerKeepsAuthenticatedInternalRequestIDAcrossRecordAndSpan(t *testing.T) {
	const trustedRequestID = "018f47a2-8a3d-4e17-9d42-6f12c89b1234"
	recorder, restore := installHTTPTestTelemetry(t)
	defer restore()
	logBuffer, restoreLogger := captureDefaultLogs()
	defer restoreLogger()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, trustedRequestID, RequestIDFromContext(r.Context()))
		_ = WithActor(r.Context(), SystemActor{ServiceName: "geul-collab"})
		w.WriteHeader(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodPost, "/internal", nil)
	request.Header.Set(RequestIDHeader, trustedRequestID)
	response := httptest.NewRecorder()
	NewHTTPHandler(mux, func(*http.Request) bool { return true }).ServeHTTP(response, request)

	require.Equal(t, trustedRequestID, response.Header().Get(RequestIDHeader))
	entry := decodeSingleLogEntry(t, logBuffer.Bytes())
	require.Equal(t, trustedRequestID, entry["request_id"])
	require.Equal(t, "system", entry["actor_kind"])
	require.Equal(t, "geul-collab", entry["actor_service"])

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	require.Contains(t, spans[0].Attributes(), attribute.String("request_id", trustedRequestID))
}

func TestHTTPHandlerReplacesMalformedAuthenticatedInternalRequestID(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal", func(w http.ResponseWriter, r *http.Request) {
		require.NotEqual(t, "malformed", RequestIDFromContext(r.Context()))
		w.WriteHeader(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodPost, "/internal", nil)
	request.Header.Set(RequestIDHeader, "malformed")
	response := httptest.NewRecorder()
	NewHTTPHandler(mux, func(*http.Request) bool { return true }).ServeHTTP(response, request)

	requestID := response.Header().Get(RequestIDHeader)
	require.NotEqual(t, "malformed", requestID)
	require.NoError(t, sharedtelemetry.ValidateRequestID(requestID))
}

func TestHTTPHandlerUsesSharedStatusClassifier(t *testing.T) {
	tests := []struct {
		statusCode  int
		wantOutcome string
		wantReason  string
	}{
		{http.StatusBadRequest, "failed", "client_error"},
		{http.StatusUnauthorized, "blocked", "authentication_required"},
		{http.StatusForbidden, "blocked", "permission_denied"},
		{http.StatusTooManyRequests, "blocked", "rate_limited"},
		{http.StatusInternalServerError, "failed", "server_error"},
	}
	for _, test := range tests {
		t.Run(http.StatusText(test.statusCode), func(t *testing.T) {
			_, restoreTelemetry := installHTTPTestTelemetry(t)
			defer restoreTelemetry()
			logBuffer, restoreLogger := captureDefaultLogs()
			defer restoreLogger()

			mux := http.NewServeMux()
			mux.HandleFunc("GET /classified", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.statusCode)
			})
			response := httptest.NewRecorder()
			NewHTTPHandler(mux, nil).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/classified", nil))

			entry := decodeSingleLogEntry(t, logBuffer.Bytes())
			require.Equal(t, test.wantOutcome, entry["outcome"])
			require.Equal(t, test.wantReason, entry["reason"])
			require.NotContains(t, entry, "error_code")
		})
	}
}

func installHTTPTestTelemetry(t *testing.T) (*tracetest.SpanRecorder, func()) {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previousProvider := otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	previousHTTPTracer := httpTracer
	previousConnectTracer := tracer
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	httpTracer = provider.Tracer("test/http")
	tracer = provider.Tracer("test/connect")
	return recorder, func() {
		httpTracer = previousHTTPTracer
		tracer = previousConnectTracer
		otel.SetTextMapPropagator(previousPropagator)
		otel.SetTracerProvider(previousProvider)
		require.NoError(t, provider.Shutdown(context.Background()))
	}
}

func captureDefaultLogs() (*bytes.Buffer, func()) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	return &output, func() { slog.SetDefault(previous) }
}

func decodeSingleLogEntry(t *testing.T, data []byte) structured.Fields {
	t.Helper()
	entries := decodeLogEntries(t, data)
	require.Len(t, entries, 1)
	return entries[0]
}

func decodeLogEntries(t *testing.T, data []byte) []structured.Fields {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	entries := make([]structured.Fields, 0, len(lines))
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var entry structured.Fields
		require.NoError(t, json.Unmarshal(line, &entry))
		entries = append(entries, entry)
	}
	return entries
}

func assertSpansExclude(t *testing.T, spans []sdktrace.ReadOnlySpan, forbidden ...string) {
	t.Helper()
	for _, span := range spans {
		for _, attr := range span.Attributes() {
			for _, value := range forbidden {
				require.NotContains(t, attr.Value.String(), value)
			}
		}
		for _, event := range span.Events() {
			for _, attr := range event.Attributes {
				for _, value := range forbidden {
					require.NotContains(t, attr.Value.String(), value)
				}
			}
		}
	}
}
