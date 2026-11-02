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

func TestListIdentitiesReadsOnlyAWellFormedTotalCount(t *testing.T) {
	for _, tc := range []struct {
		header string
		want   int64
	}{
		{header: "7", want: 7},
		{header: "12abc", want: 0},
		{header: "-3", want: 0},
		{header: "", want: 1},
	} {
		t.Run(tc.header, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.header != "" {
					w.Header().Set("X-Total-Count", tc.header)
				}
				_, _ = w.Write([]byte(`[{"id":"identity-1"}]`))
			}))
			t.Cleanup(server.Close)
			identities, total, err := NewKratosClient(server.URL).ListIdentities(t.Context(), 1, 10)
			require.NoError(t, err)
			require.Len(t, identities, 1)
			require.Equal(t, tc.want, total)
		})
	}
}
