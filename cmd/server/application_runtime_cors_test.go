package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/echovisionlab/geul-api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestApplicationRuntimeAllowsBrowserMCPProtocolVersionPreflight(t *testing.T) {
	const origin = "https://www.geul.example"
	handlerCalled := false
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(http.ResponseWriter, *http.Request) {
		handlerCalled = true
	})
	server := newApplicationHTTPServer(mux, &config.Config{
		CORSOrigins:         []string{origin},
		HTTPReadTimeoutSec:  10,
		HTTPWriteTimeoutSec: 30,
		HTTPIdleTimeoutSec:  60,
	})

	request := httptest.NewRequest(http.MethodOptions, "https://api.test/mcp", nil)
	request.Header.Set("Origin", origin)
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	request.Header.Set("Access-Control-Request-Headers", "content-type,mcp-protocol-version")
	response := httptest.NewRecorder()

	server.Handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusNoContent, response.Code)
	require.Equal(t, origin, response.Header().Get("Access-Control-Allow-Origin"))
	require.Equal(t, "true", response.Header().Get("Access-Control-Allow-Credentials"))
	require.False(t, handlerCalled, "preflight must be handled before the MCP application handler")
	require.Equal(t, "content-type,mcp-protocol-version", response.Header().Get("Access-Control-Allow-Headers"))
}
