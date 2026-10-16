package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
)

func TestFailureCauseSurvivesProductionNormalizer(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(sharedtelemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	db, scenario := newPrincipalTestDB(t, false, KratosStateActive)
	scenario.queryErr = errors.New("database unavailable person@example.test password=private")
	handler := RequireGatewaySession(db, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("X-Session-Id", testSessionID)
	handler.ServeHTTP(httptest.NewRecorder(), request)
	var entry map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
	require.Equal(t, "ERROR", entry["level"])
	require.Equal(t, "gateway_principal_resolution_failed", entry["reason"])
	require.Contains(t, entry["failure_cause"], "database unavailable")
	require.NotContains(t, output.String(), "person@example.test")
	require.NotContains(t, output.String(), "private")
}

func TestSessionFailureLogsExcludeUpstreamBody(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(sharedtelemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"name":"Private Name","token":"opaque-private-token"}`))
	}))
	t.Cleanup(upstream.Close)
	interceptor := NewAuthInterceptor(upstream.URL, nil)
	_, err := interceptor.resolveUserWithError(t.Context(), "cookie:session=private-session")
	require.ErrorContains(t, err, "kratos returned 503")
	require.Contains(t, output.String(), "kratos returned 503")
	require.NotContains(t, output.String(), "Private Name")
	require.NotContains(t, output.String(), "opaque-private-token")
	require.NotContains(t, output.String(), "private-session")
}
