package authentication

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
	"time"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
)

func captureAuthenticationFailures(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(sharedtelemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &output
}

type failingIssuancePreflight struct{ reserveErr, releaseErr error }

func (f failingIssuancePreflight) Reserve(context.Context, AuthCodeIssuanceRequest) (AuthCodeIssuanceReservation, bool, time.Duration, error) {
	return AuthCodeIssuanceReservation{}, false, 0, f.reserveErr
}
func (f failingIssuancePreflight) Release(context.Context, AuthCodeIssuanceReservation) error {
	return f.releaseErr
}

func TestProxyLogsIssuanceFailuresWithoutChangingResponses(t *testing.T) {
	output := captureAuthenticationFailures(t)
	proxy, err := NewKratosPublicProxy("http://identity.test", failingIssuancePreflight{
		reserveErr: errors.New("reserve database unavailable person@example.test token=private"),
		releaseErr: errors.New("release database unavailable"),
	}, testAuthIssuanceProvenanceKey)
	require.NoError(t, err)
	request := authenticationAccessRequest(http.MethodPost, "http://auth.test/self-service/login?flow=test", `{"method":"code","identifier":"person@example.test"}`)
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, request)
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.NotContains(t, response.Body.String(), "database unavailable")
	proxy.releaseReservation(request, AuthCodeIssuanceReservation{})
	entries := strings.Split(strings.TrimSpace(output.String()), "\n")
	require.Len(t, entries, 2)
	for i, reason := range []string{"reserve_code_issuance", "release_code_issuance"} {
		var entry map[string]any
		require.NoError(t, json.Unmarshal([]byte(entries[i]), &entry))
		require.Equal(t, reason, entry["reason"])
		require.Equal(t, "ERROR", entry["level"])
		require.Contains(t, entry["failure_cause"], "database unavailable")
		if i == 1 {
			_, hasStatus := entry["status_code"]
			require.False(t, hasStatus)
		}
	}
	require.NotContains(t, output.String(), "person@example.test")
	require.NotContains(t, output.String(), "private")
}

func TestDeniedAuthenticationLogsDiscardedAppendCause(t *testing.T) {
	output := captureAuthenticationFailures(t)
	recorder := newAuthenticationAccessTestRecorder(&authenticationAccessWriterStub{err: errors.New("security record insert unavailable")})
	handler := recorder.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"id":"invalid_credentials"}}`))
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, authenticationAccessRequest(http.MethodPost, "http://auth.test/self-service/login", `{"method":"code","code":"private-proof"}`))
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Contains(t, output.String(), "append_authentication_failure")
	require.Contains(t, output.String(), "security record insert unavailable")
	require.Contains(t, output.String(), `"flow_kind":"login"`)
	require.NotContains(t, output.String(), "private-proof")
}

// A write failure must retain the request ID and the status actually sent.
type failingAuthenticationResponseWriter struct {
	*httptest.ResponseRecorder
}

func (w failingAuthenticationResponseWriter) Write([]byte) (int, error) {
	return 0, errors.New("downstream connection closed")
}

func TestResponseWriteFailureKeepsCorrelationAndStatus(t *testing.T) {
	for _, errorResponse := range []bool{false, true} {
		t.Run(map[bool]string{false: "flow", true: "error"}[errorResponse], func(t *testing.T) {
			output := captureAuthenticationFailures(t)
			requestContext, err := sharedtelemetry.NewPropagatedRequestContext(authenticationTestSessionID, sharedtelemetry.AnonymousActor{})
			require.NoError(t, err)
			ctx := sharedtelemetry.WithRequestContext(context.Background(), requestContext)
			writer := failingAuthenticationResponseWriter{httptest.NewRecorder()}
			if errorResponse {
				writeKratosProxyError(ctx, writer, http.StatusBadRequest, "invalid request", 0)
			} else {
				copyBufferedKratosResponse(ctx, writer, bufferedKratosResponse{
					StatusCode: http.StatusBadRequest,
					Header:     make(http.Header),
					Body:       []byte(`{"code":"private-code"}`),
				}, nil)
			}
			var entry map[string]any
			require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
			require.Equal(t, authenticationTestSessionID, entry["request_id"])
			require.Equal(t, float64(http.StatusBadRequest), entry["status_code"])
			require.Equal(t, "downstream connection closed", entry["failure_cause"])
			require.NotContains(t, output.String(), "private-code")
		})
	}
}

func TestProxyTransportFailureRedactsCookieAndKeepsBadGateway(t *testing.T) {
	output := captureAuthenticationFailures(t)
	proxy, err := NewKratosPublicProxy("http://identity.test", failingIssuancePreflight{}, testAuthIssuanceProvenanceKey)
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodGet, "/self-service/login/browser", nil)
	request.AddCookie(&http.Cookie{Name: "ory_kratos_session", Value: "opaque-private-cookie"})
	response := httptest.NewRecorder()
	proxy.proxy.ErrorHandler(response, request, errors.New("invalid header value opaque-private-cookie: upstream unavailable"))
	require.Equal(t, http.StatusBadGateway, response.Code)
	require.Empty(t, response.Body.String())
	require.Contains(t, output.String(), "upstream unavailable")
	require.NotContains(t, output.String(), "opaque-private-cookie")
}
