package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRegisterMCPRoutesMountsOnlyExactGETAndPOST(t *testing.T) {
	calls := 0
	handler := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		calls++
		response.WriteHeader(http.StatusNoContent)
	})
	mux := http.NewServeMux()
	require.NoError(t, registerMCPRoutes(mux, handler))

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(method, "/mcp", nil))
		require.Equal(t, http.StatusNoContent, response.Code)
	}
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodHead, "/mcp", nil),
		httptest.NewRequest(http.MethodPut, "/mcp", nil),
		httptest.NewRequest(http.MethodGet, "/mcp/", nil),
		httptest.NewRequest(http.MethodPost, "/mcp/tools", nil),
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		require.NotEqual(t, http.StatusNoContent, response.Code)
	}
	require.Equal(t, 2, calls)

	require.Error(t, registerMCPRoutes(nil, handler))
	require.Error(t, registerMCPRoutes(http.NewServeMux(), nil))
}
