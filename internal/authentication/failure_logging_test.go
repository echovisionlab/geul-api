package authentication

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/failurecause"
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

// withFailureRecorder installs the recorder the HTTP request record reads.
func withFailureRecorder(request *http.Request) (*http.Request, *failurecause.Recorder) {
	ctx, recorder := failurecause.WithRecorder(request.Context())
	return request.WithContext(ctx), recorder
}

func failureAttr(failure failurecause.Failure, key string) string {
	for _, attr := range failure.Attrs {
		if attr.Key == key {
			return attr.Value.String()
		}
	}
	return ""
}

func TestProxyRecordsIssuanceFailuresWithoutChangingResponses(t *testing.T) {
	output := captureAuthenticationFailures(t)
	proxy, err := NewKratosPublicProxy("http://identity.test", failingIssuancePreflight{
		reserveErr: errors.New("reserve database unavailable person@example.test token=private"),
		releaseErr: errors.New("release database unavailable"),
	}, testAuthIssuanceProvenanceKey)
	require.NoError(t, err)
	request, recorder := withFailureRecorder(authenticationAccessRequest(http.MethodPost, "http://auth.test/self-service/login?flow=test", `{"method":"code","identifier":"person@example.test"}`))
	response := httptest.NewRecorder()
	proxy.ServeHTTP(response, request)
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.NotContains(t, response.Body.String(), "database unavailable")
	require.Empty(t, output.String(), "the HTTP request record logs the rejected issuance")
	failure, recorded := recorder.Recorded()
	require.True(t, recorded)
	require.Contains(t, failure.Cause, "reserve code issuance: reserve database unavailable")
	require.NotContains(t, failure.Cause, "person@example.test")
	require.NotContains(t, failure.Cause, "private")
	require.Equal(t, "login_code", failureAttr(failure, "template_type"))

	// A failed release does not change the response, so it is logged itself.
	proxy.releaseReservation(request, AuthCodeIssuanceReservation{})
	var entry map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
	require.Equal(t, "release_code_issuance", entry["operation"])
	require.Equal(t, "ERROR", entry["level"])
	require.Contains(t, entry["failure_cause"], "release database unavailable")
}

func TestFailedSuccessAppendRecordsCauseWithObservedFlow(t *testing.T) {
	output := captureAuthenticationFailures(t)
	recorder := newAuthenticationAccessTestRecorder(&authenticationAccessWriterStub{err: errors.New("security record insert unavailable")})
	handler := recorder.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"session":{"id":"` + authenticationTestSessionID + `"}}`))
	}))
	request, failures := withFailureRecorder(authenticationAccessRequest(http.MethodPost, "http://auth.test/self-service/login", `{"method":"code","code":"private-proof"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.Empty(t, output.String(), "the HTTP request record logs the failed completion")
	failure, recorded := failures.Recorded()
	require.True(t, recorded)
	require.Equal(t, "append authentication success: security record insert unavailable", failure.Cause)
	require.Equal(t, "login", failureAttr(failure, "flow_kind"))
	require.Equal(t, "email_code", failureAttr(failure, "authentication_method"))
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
	request, recorder := withFailureRecorder(httptest.NewRequest(http.MethodGet, "/self-service/login/browser", nil))
	request.AddCookie(&http.Cookie{Name: "ory_kratos_session", Value: "opaque-private-cookie"})
	response := httptest.NewRecorder()
	proxy.proxy.ErrorHandler(response, request, errors.New("invalid header value opaque-private-cookie: upstream unavailable"))
	require.Equal(t, http.StatusBadGateway, response.Code)
	require.Empty(t, response.Body.String())
	require.Empty(t, output.String(), "the HTTP request record logs the proxy failure")
	failure, recorded := recorder.Recorded()
	require.True(t, recorded)
	require.Equal(t, "proxy Kratos request: invalid header value [redacted]: upstream unavailable", failure.Cause)
}
