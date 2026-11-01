package main

import (
	"fmt"

	"connectrpc.com/connect"
	"go.akshayshah.org/connectproto"
	"google.golang.org/protobuf/encoding/protojson"

	accountadapter "github.com/echovisionlab/geul-api/internal/adapters/account"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authentication"
	authenticationpostgres "github.com/echovisionlab/geul-api/internal/authentication/postgres"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"
)

func newServiceRegistration(deps serviceRegistrationDependencies) (serviceRegistration, error) {
	mux := deps.mux
	cfg := deps.cfg
	db := deps.db
	spicedbClient := deps.spicedbClient
	telemetryWriter := deps.telemetryWriter

	// Oathkeeper validates the Kratos session and projects only the asserted
	// session id. The API resolves account_identity/member from that session.
	authInterceptor := auth.NewAuthInterceptor(cfg.KratosPublicURL, db)

	// Use connectproto for JSON codec with EmitUnpopulated to always include boolean fields
	jsonCodec := connectproto.WithJSON(
		protojson.MarshalOptions{EmitUnpopulated: true},
		protojson.UnmarshalOptions{DiscardUnknown: true},
	)

	// Request logging wraps authentication so denied requests also receive one
	// terminal record. The request scope observes the actor resolved by auth.
	// connectInterceptors places the failure boundary between the two.
	accessLogInterceptor := telemetry.NewAccessLogInterceptor(telemetryWriter)
	tracingInterceptor := telemetry.NewTracingInterceptor()
	handlerOpts := []connect.HandlerOption{
		connectInterceptors(tracingInterceptor, accessLogInterceptor, authInterceptor, auth.ExpectedMemberInterceptor(), contentsignal.ClientOriginInterceptor()),
		jsonCodec,
	}
	// Internal handlers do not use browser/session auth. Each registration below
	// is additionally wrapped in its exact caller-scoped service credential;
	// private network placement is only defense in depth. Public handlers still
	// use the auth interceptor so logged-in users can optionally hydrate context
	// for draft previews while anonymous public requests remain allowed.
	internalHandlerOpts := []connect.HandlerOption{
		connectInterceptors(tracingInterceptor, accessLogInterceptor),
		jsonCodec,
	}
	internalRPCTrust := internalRPCTrustBoundary{
		secret:                    cfg.TokenSigningSecret,
		internalServiceHeaderName: cfg.InternalServiceHeaderName,
	}
	authenticationService, err := authentication.NewService(authentication.Deps{
		Units: authenticationpostgres.NewUnits(db), SpiceDB: spicedbClient, Fence: identitystate.PostgresFence{},
		Audit: telemetryWriter, Roles: accountadapter.AccountDirectRoleTransition{}, Sessions: authenticationpostgres.NewSessions(db),
	})
	if err != nil {
		return serviceRegistration{}, fmt.Errorf("initialize authentication service: %w", err)
	}
	gatewayAuthorizationPath, gatewayAuthorizationHandler := intrav1connect.NewInternalGatewayAuthorizationServiceHandler(
		authentication.NewConnectHandler(authenticationService),
		internalHandlerOpts...,
	)
	mux.Handle(gatewayAuthorizationPath, internalRPCTrust.identity(gatewayAuthorizationHandler))
	publicHandlerOpts := []connect.HandlerOption{
		connectInterceptors(tracingInterceptor, accessLogInterceptor, authInterceptor),
		jsonCodec,
	}

	return serviceRegistration{dependencies: deps, manageOptions: handlerOpts, internalOptions: internalHandlerOpts, publicOptions: publicHandlerOpts, internalTrust: internalRPCTrust, authInterceptor: authInterceptor, authenticationService: authenticationService}, nil
}
