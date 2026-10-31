package auth

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
)

func captureFailureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(sharedtelemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &output
}

func TestGatewaySessionFailureRecordsRedactedCause(t *testing.T) {
	output := captureFailureLogs(t)
	db, scenario := newPrincipalTestDB(t, false, KratosStateActive)
	scenario.queryErr = errors.New("database unavailable person@example.test password=private")
	handler := RequireGatewaySession(db, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("X-Session-Id", testSessionID)
	ctx, recorder := failurecause.WithRecorder(request.Context())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request.WithContext(ctx))
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.Empty(t, output.String())
	failure, recorded := recorder.Recorded()
	require.True(t, recorded)
	require.Contains(t, failure.Cause, "database unavailable")
	require.NotContains(t, failure.Cause, "person@example.test")
	require.NotContains(t, failure.Cause, "private")
}

func TestSessionFailureCauseExcludesUpstreamBody(t *testing.T) {
	output := captureFailureLogs(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"name":"Private Name","token":"opaque-private-token"}`))
	}))
	t.Cleanup(upstream.Close)
	interceptor := NewAuthInterceptor(upstream.URL, nil)
	_, err := interceptor.resolveUserWithError(t.Context(), "cookie:session=private-session")
	require.ErrorContains(t, err, "kratos returned 503")
	require.Empty(t, output.String())
	cause := failurecause.Message(err)
	require.Contains(t, cause, "kratos returned 503")
	require.NotContains(t, cause, "Private Name")
	require.NotContains(t, cause, "opaque-private-token")
	require.NotContains(t, cause, "private-session")
}
