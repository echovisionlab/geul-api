package main

import (
	"fmt"
	"log/slog"
	"time"

	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/account"
	collaborationadapter "github.com/echovisionlab/geul-api/internal/adapters/collaboration"
	emaildeliveryadapter "github.com/echovisionlab/geul-api/internal/adapters/emaildelivery"
	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/maptheme"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/programevent"
	"github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"
)

// internalContentServiceRegistration keeps caller-scoped internal RPC routes
// together. Its phase stays after managed services because several internal
// handlers reuse their runtimes and application services.
type internalContentServiceRegistration struct {
	dependencies                serviceRegistrationDependencies
	handlerOptions              []connect.HandlerOption
	internalTrust               internalRPCTrustBoundary
	accountEmailChangeLifecycle *account.AccountEmailChangeLifecycle
	collaborationRuntime        *collaborationadapter.Runtime
	programEvents               *programevent.Service
	emailAuthoringUnits         mutation.UnitOfWork[emailauthoring.Store]
	legalRuntime                *legaladapter.Runtime
	legalDependencies           legal.Dependencies
	mapThemes                   *maptheme.Service
}

// internalContentServices exposes the internal services needed by later AI
// document registration; other services are owned by their registered routes.
type internalContentServices struct {
	emailAuthoring *emailauthoring.InternalService
	campaign       *campaign.InternalService
	legalDocuments *legal.AIDocumentService
}

func (r internalContentServiceRegistration) register() (internalContentServices, error) {
	deps := r.dependencies
	mux := deps.mux
	cfg := deps.cfg
	db := deps.db
	workerPublisher := deps.workerPublisher
	kratosClient := deps.kratosClient
	spicedbClient := deps.spicedbClient
	telemetryWriter := deps.telemetryWriter
	contentBlockStore := deps.contentBlockStore
	authCodeIssuanceLimiter := deps.authCodeIssuanceLimiter
	internalHandlerOpts := r.handlerOptions
	internalRPCTrust := r.internalTrust
	accountEmailChangeLifecycle := r.accountEmailChangeLifecycle
	collaborationRuntime := r.collaborationRuntime
	legalRuntime := r.legalRuntime
	legalDependencies := r.legalDependencies
	// Internal API services are not browser/session authenticated and must not
	// be exposed via Oathkeeper. Every handler is protected below by the exact
	// caller-scoped service credential; private-network placement is additional
	// defense in depth.

	// EmailCourierService - authenticated identity courier ingress.
	emailCourierService := emaildelivery.NewEmailCourierService(
		workerPublisher,
		kratosClient,
		emaildeliveryadapter.NewAuthIssuanceAuthority(
			[]byte(cfg.TokenSigningSecret),
			authCodeIssuanceLimiter,
			accountEmailChangeLifecycle,
		),
		time.Duration(cfg.AuthCodeLifespanSeconds)*time.Second,
		emaildelivery.WithVerificationUIURL(cfg.SiteOrigin+"/verify"),
	)
	emailCourierPath, emailCourierHandler := intrav1connect.NewEmailCourierServiceHandler(emaildelivery.NewCourierConnectHandler(emailCourierService), internalHandlerOpts...)
	mux.Handle(emailCourierPath, internalRPCTrust.identity(emailCourierHandler))
	slog.Info("Registered internal service", "path", emailCourierPath)
	internalProgramEventPath, internalProgramEventHandler := intrav1connect.NewInternalProgramEventServiceHandler(programevent.NewInternalConnectHandler(r.programEvents), internalHandlerOpts...)
	mux.Handle(internalProgramEventPath, internalRPCTrust.collab(internalProgramEventHandler))
	slog.Info("Registered internal service", "path", internalProgramEventPath)

	internalMapPath, internalMapHandler := intrav1connect.NewInternalMapServiceHandler(maptheme.NewInternalConnectHandler(r.mapThemes), internalHandlerOpts...)
	mux.Handle(internalMapPath, internalRPCTrust.collab(internalMapHandler))
	slog.Info("Registered internal service", "path", internalMapPath)

	internalCampaignService, err := campaign.NewInternalService(campaign.InternalDeps{
		DB: db, SpiceDB: spicedbClient, Permissions: spicedbClient, ContentBlocks: contentBlockStore,
		Checkpoints: collaborationRuntime.Checkpoints, Members: memberadapter.ContributorMembers{}, Audit: telemetryWriter,
	})
	if err != nil {
		return internalContentServices{}, fmt.Errorf("initialize internal campaign service: %w", err)
	}
	internalCampaignPath, internalCampaignHandler := intrav1connect.NewInternalCampaignServiceHandler(campaign.NewInternalConnectHandler(internalCampaignService), internalHandlerOpts...)
	mux.Handle(internalCampaignPath, internalRPCTrust.collab(internalCampaignHandler))
	slog.Info("Registered internal service", "path", internalCampaignPath)

	internalEmailAuthoringService, err := emailauthoring.NewInternalService(emailauthoring.InternalDeps{
		Units: r.emailAuthoringUnits, SpiceDB: spicedbClient, Fence: identitystate.PostgresFence{}, Audit: telemetryWriter,
	})
	if err != nil {
		return internalContentServices{}, fmt.Errorf("initialize internal email authoring service: %w", err)
	}
	internalEmailTemplatePath, internalEmailTemplateHandler := intrav1connect.NewInternalEmailTemplateServiceHandler(emailauthoring.NewInternalTemplateConnectHandler(internalEmailAuthoringService), internalHandlerOpts...)
	mux.Handle(internalEmailTemplatePath, internalRPCTrust.collab(internalEmailTemplateHandler))
	slog.Info("Registered internal service", "path", internalEmailTemplatePath)

	internalEmailLayoutPath, internalEmailLayoutHandler := intrav1connect.NewInternalEmailLayoutServiceHandler(emailauthoring.NewInternalLayoutConnectHandler(internalEmailAuthoringService), internalHandlerOpts...)
	mux.Handle(internalEmailLayoutPath, internalRPCTrust.collab(internalEmailLayoutHandler))
	slog.Info("Registered internal service", "path", internalEmailLayoutPath)

	internalLegalService, err := legal.NewInternalService(legal.InternalDeps{
		DB:            db,
		ContentBlocks: contentBlockStore,
		SpiceDB:       spicedbClient,
		Checkpoints:   collaborationRuntime.Checkpoints,
		OG:            legalDependencies.OG,
		Audit:         telemetryWriter,
	})
	if err != nil {
		return internalContentServices{}, fmt.Errorf("initialize internal legal service: %w", err)
	}
	internalTermsPath, internalTermsHandler := intrav1connect.NewInternalTermsServiceHandler(legal.NewInternalTermsConnectHandler(internalLegalService), internalHandlerOpts...)
	mux.Handle(internalTermsPath, internalRPCTrust.collab(internalTermsHandler))
	slog.Info("Registered internal service", "path", internalTermsPath)

	internalPrivacyPath, internalPrivacyHandler := intrav1connect.NewInternalPrivacyServiceHandler(legal.NewInternalPrivacyConnectHandler(internalLegalService), internalHandlerOpts...)
	mux.Handle(internalPrivacyPath, internalRPCTrust.collab(internalPrivacyHandler))
	slog.Info("Registered internal service", "path", internalPrivacyPath)

	legalAIDocumentService, err := legal.NewAIDocumentService(legal.AIDocumentDeps{
		DB: db, ContentBlocks: contentBlockStore, SpiceDB: spicedbClient, OG: legalRuntime, Audit: telemetryWriter,
	})
	if err != nil {
		return internalContentServices{}, fmt.Errorf("initialize Legal AI document service: %w", err)
	}
	return internalContentServices{
		emailAuthoring: internalEmailAuthoringService,
		campaign:       internalCampaignService,
		legalDocuments: legalAIDocumentService,
	}, nil
}
