//go:build integration

package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/echovisionlab/geul-api/internal/account"
	accountadapter "github.com/echovisionlab/geul-api/internal/adapters/account"
	authenticationadapter "github.com/echovisionlab/geul-api/internal/adapters/authentication"
	emaildeliveryadapter "github.com/echovisionlab/geul-api/internal/adapters/emaildelivery"
	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authentication"
	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/handler"
	"github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/structured"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"
	"github.com/testcontainers/testcontainers-go"
)

const integrationInternalServiceHeaderName = "X-Internal-Service"

func main() {
	ctx := context.Background()
	hookServer, hookBaseURL, err := startHookServer()
	if err != nil {
		fmt.Fprintf(os.Stderr, "start backend integration hook server: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if err := hookServer.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "cleanup backend integration hook server: %v\n", err)
		}
	}()

	stack, err := testutil.StartBackendIntegrationStackWithOptions(ctx, testutil.BackendIntegrationStackOptions{
		HookBaseURL: hookBaseURL,
		Logf: func(format string, args ...structured.Value) {
			fmt.Fprintf(os.Stderr, format+"\n", args...)
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "start backend integration stack: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if err := stack.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "cleanup backend integration stack: %v\n", err)
		}
	}()

	publisher, err := mq.NewPublisher(stack.Postgres.SQLDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start backend integration hook publisher: %v\n", err)
		os.Exit(1)
	}
	telemetryWriter := apitelemetry.NewDurableWriter(stack.Postgres.DB)
	directRoles := accountadapter.AccountDirectRoleTransition{}
	accountEmailChangeLifecycle := account.NewAuditedAccountEmailChangeLifecycle(
		stack.Postgres.DB,
		stack.KratosClient,
		publisher,
		accountadapter.MemberEmailProjection{},
		telemetryWriter,
	)
	go accountEmailChangeLifecycle.Start(ctx)
	registrationMembers := member.NewMemberProvisioner(
		stack.Postgres.DB,
		stack.KratosClient,
		stack.SpiceDBClient,
		memberadapter.AccountEmailProjection{},
		directRoles,
	)
	loginHooks := authentication.NewLoginHookService(
		stack.KratosClient,
		authenticationadapter.NewLoginMemberProvisioner(registrationMembers),
		authentication.NewAuthBootstrapService(
			stack.Postgres.DB,
			stack.SpiceDBClient,
			telemetryWriter,
			directRoles,
		),
	)
	registrationHooks := authentication.NewRegistrationHookPolicy(
		authenticationadapter.NewRegistrationReuseHoldChecker(stack.Postgres.DB),
	)
	accountSettingsHooks := account.NewAccountSettingsHookService(
		stack.KratosClient,
		accountEmailChangeLifecycle,
	)
	credentialHooks := account.NewAccountCredentialHookLifecycle(
		stack.Postgres.DB,
		stack.KratosClient,
		telemetryWriter,
		publisher,
		accountadapter.MemberEmailProjection{},
	)
	hooksHandler := handler.NewHooksHandler(
		loginHooks,
		registrationHooks,
		accountSettingsHooks,
		credentialHooks,
	)
	_, rawCourierHandler := intrav1connect.NewEmailCourierServiceHandler(
		emaildelivery.NewEmailCourierService(
			publisher,
			stack.KratosClient,
			emaildeliveryadapter.NewAuthIssuanceAuthority([]byte(stack.TokenSigningSecret), nil, nil),
			15*time.Minute,
		),
	)
	courierHandler := protectBackendIntegrationCourier(
		stack.TokenSigningSecret,
		rawCourierHandler,
	)
	hookServer.SetHandlers(hooksHandler, courierHandler)

	fmt.Fprintln(os.Stderr, "backend integration stack ready")

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	<-signals
}

type hookServer struct {
	server   *http.Server
	listener net.Listener

	mu             sync.RWMutex
	hooksHandler   *handler.HooksHandler
	courierHandler http.Handler
}

func startHookServer() (*hookServer, string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", err
	}

	server := &hookServer{listener: listener}
	mux := http.NewServeMux()
	protectHook := func(h http.Handler) http.Handler {
		return auth.RequireInternalServiceSecret(
			testutil.IntegrationTokenSigningSecret,
			integrationInternalServiceHeaderName,
			h,
		)
	}
	mux.Handle("/hooks/after-login", protectHook(server.callHook(func(h *handler.HooksHandler, w http.ResponseWriter, r *http.Request) {
		h.AfterLogin(w, r)
	})))
	mux.Handle("/hooks/reject-credential-registration", protectHook(server.callHook(func(h *handler.HooksHandler, w http.ResponseWriter, r *http.Request) {
		h.RejectCredentialRegistration(w, r)
	})))
	mux.Handle("/hooks/pre-settings-oidc", protectHook(server.callHook(func(h *handler.HooksHandler, w http.ResponseWriter, r *http.Request) {
		h.PreSettingsOIDC(w, r)
	})))
	mux.Handle("/hooks/post-settings-oidc", protectHook(server.callHook(func(h *handler.HooksHandler, w http.ResponseWriter, r *http.Request) {
		h.PostSettingsOIDC(w, r)
	})))
	mux.Handle("/hooks/pre-settings-passkey", protectHook(server.callHook(func(h *handler.HooksHandler, w http.ResponseWriter, r *http.Request) {
		h.PreSettingsPasskey(w, r)
	})))
	mux.Handle("/hooks/post-settings-passkey", protectHook(server.callHook(func(h *handler.HooksHandler, w http.ResponseWriter, r *http.Request) {
		h.PostSettingsPasskey(w, r)
	})))
	mux.Handle("/hooks/after-settings", protectHook(server.callHook(func(h *handler.HooksHandler, w http.ResponseWriter, r *http.Request) {
		h.AfterSettings(w, r)
	})))
	mux.Handle("/hooks/after-verification", protectHook(server.callHook(func(h *handler.HooksHandler, w http.ResponseWriter, r *http.Request) {
		h.AfterVerification(w, r)
	})))
	mux.Handle("/api.intra.v1.EmailCourierService/", http.HandlerFunc(server.callCourier))

	server.server = &http.Server{Handler: mux}
	errCh := make(chan error, 1)
	go func() {
		if err := server.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		_ = listener.Close()
		return nil, "", err
	default:
		_, port, err := net.SplitHostPort(listener.Addr().String())
		if err != nil {
			_ = listener.Close()
			return nil, "", err
		}
		return server, "http://" + net.JoinHostPort(testcontainers.HostInternal, port), nil
	}
}

func (s *hookServer) SetHandlers(hooksHandler *handler.HooksHandler, courierHandler http.Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hooksHandler = hooksHandler
	s.courierHandler = courierHandler
}

func (s *hookServer) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.server.Shutdown(ctx)
}

func (s *hookServer) callHook(fn func(*handler.HooksHandler, http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		hooksHandler := s.hooksHandler
		s.mu.RUnlock()
		if hooksHandler == nil {
			http.Error(w, "backend integration hooks are not ready", http.StatusServiceUnavailable)
			return
		}
		fn(hooksHandler, w, r)
	}
}

func (s *hookServer) callCourier(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	courierHandler := s.courierHandler
	s.mu.RUnlock()
	if courierHandler == nil {
		http.Error(w, "backend integration courier is not ready", http.StatusServiceUnavailable)
		return
	}
	courierHandler.ServeHTTP(w, r)
}

func protectBackendIntegrationCourier(secret string, handler http.Handler) http.Handler {
	return auth.RequireInternalServiceSecret(secret, integrationInternalServiceHeaderName, handler)
}
