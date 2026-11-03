package main

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/echovisionlab/geul-api/internal/adapters/hydra"
	membercriteriaadapter "github.com/echovisionlab/geul-api/internal/adapters/membercriteria"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/oauthapp"
	oauthapppostgres "github.com/echovisionlab/geul-api/internal/oauthapp/postgres"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

// registerOAuthApps registers the sign-in app and connected AI tool
// management, and the sign-in decision the login app asks for.
func (r serviceRegistration) registerOAuthApps() (*oauthapp.Service, error) {
	deps := r.dependencies
	units, err := oauthapppostgres.NewUnits(deps.db)
	if err != nil {
		return nil, fmt.Errorf("initialize OAuth app store: %w", err)
	}
	clients, err := hydra.NewClients(deps.cfg.HydraAdminURL, &http.Client{})
	if err != nil {
		return nil, fmt.Errorf("initialize Hydra Admin client: %w", err)
	}
	service, err := oauthapp.NewService(oauthapp.Deps{
		Units: units, SpiceDB: deps.spicedbClient, Fence: identitystate.PostgresFence{}, Audit: deps.telemetryWriter,
		Clients: clients, Eligibility: membercriteriaadapter.NewSignInEligibility(deps.db, deps.spicedbClient),
		MCPResourceURL: deps.cfg.SiteOrigin + "/mcp",
	})
	if err != nil {
		return nil, fmt.Errorf("initialize OAuth app service: %w", err)
	}
	path, handler := managev1connect.NewOAuthAppServiceHandler(oauthapp.NewConnectHandler(service), r.manageOptions...)
	deps.mux.Handle(path, handler)
	slog.Info("Registered service", "path", path)
	return service, nil
}
