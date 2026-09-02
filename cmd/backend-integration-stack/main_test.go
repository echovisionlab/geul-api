//go:build integration

package main

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// MAIL-02: the integration ingress uses the same authenticated boundary as production.
func TestBackendIntegrationCourierRouteRequiresInternalServiceSecret(t *testing.T) {
	server, _, err := startHookServer()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, server.Close()) })

	const secret = "backend-integration-test-secret"
	handler := protectBackendIntegrationCourier(
		secret,
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
	)
	server.SetHandlers(nil, handler)
	endpoint := "http://" + server.listener.Addr().String() +
		"/api.intra.v1.EmailCourierService/SendEmail"

	for _, tc := range []struct {
		name       string
		secret     string
		wantStatus int
	}{
		{name: "missing secret", wantStatus: http.StatusUnauthorized},
		{name: "wrong secret", secret: "wrong-secret", wantStatus: http.StatusUnauthorized},
		{name: "exact secret", secret: secret, wantStatus: http.StatusNoContent},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			request, err := http.NewRequest(
				http.MethodPost,
				endpoint,
				strings.NewReader(`{}`),
			)
			require.NoError(t, err)
			if tc.secret != "" {
				request.Header.Set(integrationInternalServiceHeaderName, tc.secret)
			}
			response, err := http.DefaultClient.Do(request)
			require.NoError(t, err)
			defer response.Body.Close()
			_, err = io.Copy(io.Discard, response.Body)
			require.NoError(t, err)
			require.Equal(t, tc.wantStatus, response.StatusCode)
		})
	}
}
