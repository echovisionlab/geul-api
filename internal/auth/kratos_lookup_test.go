package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetIdentityPreservesHTTPFailureStatus(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":"private-upstream-response"}`))
			}))
			t.Cleanup(server.Close)
			identity, err := NewKratosClient(server.URL).GetIdentity(t.Context(), "identity-1")
			require.Nil(t, identity)
			var upstreamErr *KratosHTTPError
			require.ErrorAs(t, err, &upstreamErr)
			require.Equal(t, status, upstreamErr.StatusCode)
			if status == http.StatusNotFound {
				require.EqualError(t, err, "identity not found: identity-1")
				require.NotContains(t, err.Error(), "private-upstream-response")
				require.Contains(t, upstreamErr.Body, "private-upstream-response")
			}
		})
	}
}
