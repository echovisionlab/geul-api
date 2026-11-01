package main

import (
	"fmt"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/ai"
	aipostgres "github.com/echovisionlab/geul-api/internal/ai/postgres"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

func (r serviceRegistration) registerAI() error {
	deps := r.dependencies
	aiUnits, err := aipostgres.NewUnits(deps.db, deps.servicePublisher)
	if err != nil {
		return fmt.Errorf("initialize AI store: %w", err)
	}
	aiService, err := ai.NewService(ai.Deps{
		Units: aiUnits, SpiceDB: deps.spicedbClient, Fence: identitystate.PostgresFence{}, Audit: deps.telemetryWriter,
	})
	if err != nil {
		return fmt.Errorf("initialize AI service: %w", err)
	}
	aiPath, aiHandler := managev1connect.NewAIServiceHandler(ai.NewConnectHandler(aiService), r.manageOptions...)
	deps.mux.Handle(aiPath, aiHandler)
	slog.Info("Registered service", "path", aiPath)

	return nil
}
