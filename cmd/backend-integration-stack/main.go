//go:build integration

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/testcontainers/testcontainers-go"

	"github.com/echovisionlab/geul-api/internal/account"
	accountpostgres "github.com/echovisionlab/geul-api/internal/account/postgres"
	accountadapter "github.com/echovisionlab/geul-api/internal/adapters/account"
	authenticationadapter "github.com/echovisionlab/geul-api/internal/adapters/authentication"
	emaildeliveryadapter "github.com/echovisionlab/geul-api/internal/adapters/emaildelivery"
	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authentication"
	authenticationpostgres "github.com/echovisionlab/geul-api/internal/authentication/postgres"
	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/member"
	memberpostgres "github.com/echovisionlab/geul-api/internal/member/postgres"
	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/structured"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"
)

const integrationInternalServiceHeaderName = "X-Internal-Service"

func main() {
	ctx := context.Background()
	hookServer, hookBaseURL, err := startHookServer(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start backend integration hook server: %v\n", failurecause.Message(err))
		os.Exit(1)
	}
	defer func() {
		if err := hookServer.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "cleanup backend integration hook server: %v\n", failurecause.Message(err))
		}
	}()

	stack, err := testutil.StartBackendIntegrationStackWithOptions(ctx, testutil.BackendIntegrationStackOptions{
		HookBaseURL: hookBaseURL,
		Logf: func(format string, args ...structured.Value) {
			fmt.Fprintf(os.Stderr, format+"\n", args...)
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "start backend integration stack: %v\n", failurecause.Message(err))
		os.Exit(1)
	}
	defer func() {
		if err := stack.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "cleanup backend integration stack: %v\n", failurecause.Message(err))
		}
	}()

	publisher, err := mq.NewPublisher(stack.Postgres.SQLDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start backend integration hook publisher: %v\n", failurecause.Message(err))
		os.Exit(1)
	}
	telemetryWriter := apitelemetry.NewDurableWriter(stack.Postgres.DB)
	directRoles := accountadapter.AccountDirectRoleTransition{}
	accountUnits, err := accountpostgres.NewUnits(stack.Postgres.DB, accountpostgres.Ports{
		Members: accountadapter.MemberDeletion{}, MemberEmails: accountadapter.MemberEmailProjection{},
		Newsletter: accountadapter.NewsletterSubscription{}, Publisher: publisher,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "start backend integration account units: %v\n", failurecause.Message(err))
		os.Exit(1)
	}
	accountEmailChangeLifecycle := account.NewAccountEmailChangeLifecycle(
		accountUnits,
		stack.KratosClient,
		accountpostgres.EmailCommands{Publisher: publisher},
	)
	go accountEmailChangeLifecycle.Start(ctx)
	accountEmailProjection := memberadapter.AccountEmailProjection{Units: accountUnits, Identity: stack.KratosClient}
	memberUnits, err := memberpostgres.NewUnits(stack.Postgres.DB, memberpostgres.Ports{
		WelcomeEmails: publisher, WelcomeRecipients: accountEmailProjection,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "start backend integration member units: %v\n", failurecause.Message(err))
		os.Exit(1)
	}
	registrationMembers := member.NewMemberProvisioner(
		memberUnits,
		stack.KratosClient,
		stack.SpiceDBClient,
		accountEmailProjection,
		directRoles,
	)
	loginRoles, err := authentication.NewService(authentication.Deps{
		Units: authenticationpostgres.NewUnits(stack.Postgres.DB), SpiceDB: stack.SpiceDBClient, Fence: identitystate.PostgresFence{},
		Audit: telemetryWriter, Roles: directRoles, Sessions: authenticationpostgres.NewSessions(stack.Postgres.DB),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "start backend integration login roles: %v\n", failurecause.Message(err))
		os.Exit(1)
	}
	loginHooks := authentication.NewLoginHookService(
		stack.KratosClient,
		authenticationadapter.NewLoginMemberProvisioner(registrationMembers),
		loginRoles,
	)
	registrationHooks := authentication.NewRegistrationHookPolicy(
		authenticationadapter.NewRegistrationReuseHoldChecker(stack.Postgres.DB),
	)
	accountSettingsHooks := account.NewAccountSettingsHookService(
		stack.KratosClient,
		accountEmailChangeLifecycle,
	)
	credentialHooks := account.NewAccountCredentialHookLifecycle(
		accountUnits,
		stack.KratosClient,
		telemetryWriter,
		accountpostgres.EmailCommands{Publisher: publisher},
	)
	authenticationHooks := authenticationadapter.NewHooksHandler(loginHooks, registrationHooks)
	accountHooks := accountadapter.NewSettingsHooksHandler(accountSettingsHooks, credentialHooks)
	_, rawCourierHandler := intrav1connect.NewEmailCourierServiceHandler(
		emaildelivery.NewCourierConnectHandler(emaildelivery.NewEmailCourierService(
			publisher,
			stack.KratosClient,
			emaildeliveryadapter.NewAuthIssuanceAuthority([]byte(stack.TokenSigningSecret), nil, nil),
			15*time.Minute,
		)),
	)
	courierHandler := protectBackendIntegrationCourier(
		stack.TokenSigningSecret,
		rawCourierHandler,
	)
	hookServer.SetHandlers(authenticationHooks, accountHooks, courierHandler)

	fmt.Fprintln(os.Stderr, "backend integration stack ready")

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	<-signals
}

type hookServer struct {
	server   *http.Server
	listener net.Listener

	mu             sync.RWMutex
	hooksHandler   http.Handler
	courierHandler http.Handler
}

func startHookServer(ctx context.Context) (*hookServer, string, error) {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
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
	mux.Handle("/api.intra.v1.EmailCourierService/", http.HandlerFunc(server.callCourier))

	mux.Handle("/hooks/", protectHook(http.HandlerFunc(server.callHook)))
	server.server = &http.Server{Handler: mux}
	errCh := make(chan error, 1)
	go func() {
		if err := server.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			slog.ErrorContext(ctx, "Backend integration hook server failed", "failure_cause", failurecause.Message(err))
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		if closeErr := listener.Close(); closeErr != nil {
			slog.ErrorContext(ctx, "Backend integration listener close failed", "failure_cause", failurecause.Message(closeErr))
		}
		return nil, "", err
	default:
		_, port, err := net.SplitHostPort(listener.Addr().String())
		if err != nil {
			if closeErr := listener.Close(); closeErr != nil {
				slog.ErrorContext(ctx, "Backend integration listener close failed", "failure_cause", failurecause.Message(closeErr))
			}
			return nil, "", err
		}
		return server, "http://" + net.JoinHostPort(testcontainers.HostInternal, port), nil
	}
}

func (s *hookServer) SetHandlers(authenticationHooks *authenticationadapter.HooksHandler, accountHooks *accountadapter.SettingsHooksHandler, courierHandler http.Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if authenticationHooks != nil && accountHooks != nil {
		mux := http.NewServeMux()
		direct := func(h http.HandlerFunc) http.Handler { return h }
		authenticationHooks.RegisterRoutes(mux, direct)
		accountHooks.RegisterRoutes(mux, direct)
		s.hooksHandler = mux
	} else {
		s.hooksHandler = nil
	}
	s.courierHandler = courierHandler
}

func (s *hookServer) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.server.Shutdown(ctx)
}

func (s *hookServer) callHook(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	hooksHandler := s.hooksHandler
	s.mu.RUnlock()
	if hooksHandler == nil {
		http.Error(w, "backend integration hooks are not ready", http.StatusServiceUnavailable)
		return
	}
	hooksHandler.ServeHTTP(w, r)
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
