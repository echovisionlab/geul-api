package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authentication"
	"github.com/echovisionlab/geul-api/internal/config"
	"github.com/echovisionlab/geul-api/internal/telemetry"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
)

const testInternalServiceHeaderName = "X-Internal-Service"

func TestInternalRPCTrustBoundaryUsesCanonicalTokenSigningSecret(t *testing.T) {
	t.Parallel()

	const tokenSigningSecret = "token-signing-secret"
	boundary := internalRPCTrustBoundary{secret: tokenSigningSecret, internalServiceHeaderName: testInternalServiceHeaderName}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	for _, scope := range []struct {
		name string
		wrap func(http.Handler) http.Handler
	}{
		{
			name: "collaboration",
			wrap: boundary.collab,
		},
		{
			name: "identity courier",
			wrap: boundary.identity,
		},
		{
			name: "OG worker",
			wrap: boundary.og,
		},
	} {
		t.Run(scope.name, func(t *testing.T) {
			t.Parallel()

			protected := scope.wrap(next)
			for _, attempt := range []string{"", "wrong-secret"} {
				req := httptest.NewRequest(http.MethodPost, "/api.intra.v1.TestService/Call", nil)
				if attempt != "" {
					req.Header.Set(testInternalServiceHeaderName, attempt)
				}
				resp := httptest.NewRecorder()

				protected.ServeHTTP(resp, req)

				require.Equal(t, http.StatusUnauthorized, resp.Code, "secret %q must be rejected", attempt)
			}

			req := httptest.NewRequest(http.MethodPost, "/api.intra.v1.TestService/Call", nil)
			req.Header.Set(testInternalServiceHeaderName, tokenSigningSecret)
			resp := httptest.NewRecorder()

			protected.ServeHTTP(resp, req)

			require.Equal(t, http.StatusNoContent, resp.Code)
		})
	}
}

func TestInternalRPCTrustBoundaryPropagatesCorrelationAndSystemActor(t *testing.T) {
	t.Parallel()

	const (
		secret    = "token-signing-secret"
		requestID = "018f47a2-8a3d-4e17-9d42-6f12c89b1234"
	)
	mux := http.NewServeMux()
	mux.Handle("POST /internal", internalRPCTrustBoundary{secret: secret, internalServiceHeaderName: testInternalServiceHeaderName}.collab(http.HandlerFunc(
		func(w http.ResponseWriter, request *http.Request) {
			requestContext, ok := telemetry.RequestContextFrom(request.Context())
			require.True(t, ok)
			require.Equal(t, requestID, requestContext.RequestID)
			require.Equal(t, "system", string(requestContext.Actor.Kind()))
			require.Equal(t, sharedtelemetry.ServiceEditorCollab, requestContext.Actor.(telemetry.SystemActor).ServiceName)
			w.WriteHeader(http.StatusNoContent)
		},
	)))
	handler := telemetry.NewHTTPHandler(mux, func(request *http.Request) bool {
		return auth.IsInternalServiceRequest(secret, testInternalServiceHeaderName, request)
	})
	request := httptest.NewRequest(http.MethodPost, "/internal", nil)
	request.Header.Set(testInternalServiceHeaderName, secret)
	request.Header.Set(telemetry.RequestIDHeader, requestID)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusNoContent, response.Code)
	require.Equal(t, requestID, response.Header().Get(telemetry.RequestIDHeader))
}

func TestHealthRoutesPreserveDeployedReadinessAndSeparateLiveness(t *testing.T) {
	t.Parallel()

	readyErr := error(nil)
	readyCalls := 0
	mux := http.NewServeMux()
	registerHealthRoutes(mux, func(context.Context) error {
		readyCalls++
		return readyErr
	})

	assertHealthResponse(t, mux, "/health/live", http.StatusOK, "ok\n")
	require.Zero(t, readyCalls)
	assertHealthResponse(t, mux, "/health", http.StatusOK, "ok\n")
	assertHealthResponse(t, mux, "/health/ready", http.StatusOK, "ok\n")
	require.Equal(t, 2, readyCalls)

	readyErr = errors.New("PostgreSQL unavailable")
	assertHealthResponse(t, mux, "/health", http.StatusServiceUnavailable, "not ready\n")
	assertHealthResponse(t, mux, "/health/ready", http.StatusServiceUnavailable, "not ready\n")
	assertHealthResponse(t, mux, "/health/live", http.StatusOK, "ok\n")
	require.Equal(t, 4, readyCalls)
}

func TestPostgresPGMQReadinessRequiresDatabase(t *testing.T) {
	t.Parallel()

	check := newPostgresPGMQReadinessCheck(nil)
	require.ErrorContains(t, check(t.Context()), "PostgreSQL connection is required")
}

func assertHealthResponse(
	t *testing.T,
	handler http.Handler,
	path string,
	wantStatus int,
	wantBody string,
) {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, wantStatus, response.Code)
	require.Equal(t, wantBody, response.Body.String())
}

func TestAwaitShutdownCancelsRootLifecycleOnRuntimeFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	failures := make(chan error, 1)
	signals := make(chan os.Signal)
	expected := errors.New("consumer delivery channel closed")
	reportRuntimeFailure(failures, expected)

	signal, runtimeErr := awaitShutdown(ctx, cancel, signals, failures)

	require.Nil(t, signal)
	require.ErrorIs(t, runtimeErr, expected)
	require.ErrorIs(t, ctx.Err(), context.Canceled)
}

func TestReportRuntimeFailureKeepsFirstFailureWithoutBlocking(t *testing.T) {
	failures := make(chan error, 1)
	first := errors.New("first")
	reportRuntimeFailure(failures, first)
	reportRuntimeFailure(failures, errors.New("second"))
	reportRuntimeFailure(failures, nil)

	require.ErrorIs(t, <-failures, first)
}

func TestMCPPrivateHTTPServerExposesOnlyExactRoutes(t *testing.T) {
	admissionCalls := 0
	admissionHandler := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		admissionCalls++
		response.WriteHeader(http.StatusNoContent)
	})
	server, err := newMCPPrivateHTTPServer(mcpPrivateHandlers{
		authorAdmission: admissionHandler,
	}, &config.Config{
		MCPPrivatePort:      8001,
		HTTPReadTimeoutSec:  10,
		HTTPWriteTimeoutSec: 30,
		HTTPIdleTimeoutSec:  60,
	})
	require.NoError(t, err)
	require.Equal(t, ":8001", server.Addr)

	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, authentication.MCPGatewayAuthorAdmissionPath, nil))
	require.Equal(t, http.StatusNoContent, response.Code)
	require.Equal(t, 1, admissionCalls)

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, authentication.MCPGatewayAuthorAdmissionPath, nil),
		httptest.NewRequest(http.MethodPost, authentication.MCPGatewayAuthorAdmissionPath+"/extra", nil),
		httptest.NewRequest(http.MethodGet, "/internal/mcp/pat/whoami", nil),
		httptest.NewRequest(http.MethodGet, "/health", nil),
	} {
		response = httptest.NewRecorder()
		server.Handler.ServeHTTP(response, request)
		require.NotEqual(t, http.StatusNoContent, response.Code)
	}
	require.Equal(t, 1, admissionCalls)

	_, err = newMCPPrivateHTTPServer(mcpPrivateHandlers{}, &config.Config{})
	require.Error(t, err)
	_, err = newMCPPrivateHTTPServer(mcpPrivateHandlers{
		authorAdmission: admissionHandler,
	}, nil)
	require.Error(t, err)
}

func TestApplicationRuntimeReportsMCPPrivateListenerFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	runtime := &applicationRuntime{}
	failures := make(chan error, 1)
	runtime.startHTTPServer("MCP private HTTP server", &http.Server{
		Addr: listener.Addr().String(), Handler: http.NewServeMux(),
	}, failures)

	select {
	case runtimeErr := <-failures:
		require.ErrorContains(t, runtimeErr, "MCP private HTTP server")
	case <-time.After(2 * time.Second):
		t.Fatal("private listener failure was not reported")
	}
	require.True(t, waitForShutdownWorkers(t.Context(), &runtime.wg))
}

func TestApplicationRuntimeShutdownStopsBothHTTPServers(t *testing.T) {
	runtime := &applicationRuntime{}
	failures := make(chan error, 1)
	mainServer := &http.Server{Addr: "127.0.0.1:0", Handler: http.NewServeMux()}
	mcpPrivateServer := &http.Server{Addr: "127.0.0.1:0", Handler: http.NewServeMux()}
	runtime.startHTTPServer("HTTP server", mainServer, failures)
	runtime.startHTTPServer("MCP private HTTP server", mcpPrivateServer, failures)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	require.True(t, runtime.Shutdown(ctx, mainServer, mcpPrivateServer))
	select {
	case runtimeErr := <-failures:
		t.Fatalf("graceful dual-server shutdown reported failure: %v", runtimeErr)
	default:
	}
}

func TestWaitForShutdownWorkersReturnsWhenWorkerStops(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	wg.Done()

	require.True(t, waitForShutdownWorkers(t.Context(), &wg))
}

func TestWaitForShutdownWorkersHonorsBoundedShutdown(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)

	require.False(t, waitForShutdownWorkers(ctx, &wg))
	wg.Done()
}
