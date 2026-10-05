package main

import (
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/account"
	collaborationadapter "github.com/echovisionlab/geul-api/internal/adapters/collaboration"
	emailauthoringadapter "github.com/echovisionlab/geul-api/internal/adapters/emailauthoring"
	emaildeliveryadapter "github.com/echovisionlab/geul-api/internal/adapters/emaildelivery"
	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	pageadapter "github.com/echovisionlab/geul-api/internal/adapters/page"
	postruntime "github.com/echovisionlab/geul-api/internal/adapters/post/runtime"
	programeventadapter "github.com/echovisionlab/geul-api/internal/adapters/programevent"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	formdomain "github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/maptheme"
	"github.com/echovisionlab/geul-api/internal/page"
	"github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/programevent"
	"github.com/echovisionlab/geul-api/internal/work"
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
	pageRuntime                 *pageadapter.Runtime
	workRuntime                 *workadapter.Runtime
	fileService                 *filemedia.FileService
	postMedia                   postruntime.ContentBlockMedia
	programEventFiles           *programeventadapter.Files
	emailAuthoringReferences    *emailauthoringadapter.CampaignDeliveryReferences
	legalRuntime                *legaladapter.Runtime
	legalDependencies           legal.Dependencies
	formDependencies            formdomain.Dependencies
}

// internalContentServices exposes the internal services needed by later AI
// document registration; other services are owned by their registered routes.
type internalContentServices struct {
	page           *page.InternalPageService
	work           *work.InternalWorkService
	emailTemplate  *emailauthoring.InternalEmailTemplateService
	campaign       *campaign.InternalCampaignService
	form           *formdomain.InternalFormService
	legalDocuments *legal.AIDocumentService
}

func (r internalContentServiceRegistration) register() (internalContentServices, error) {
	deps := r.dependencies
	mux := deps.mux
	cfg := deps.cfg
	db := deps.db
	servicePublisher := deps.servicePublisher
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
	pageRuntime := r.pageRuntime
	workRuntime := r.workRuntime
	fileService := r.fileService
	postMedia := r.postMedia
	programEventFiles := r.programEventFiles
	emailAuthoringReferences := r.emailAuthoringReferences
	legalRuntime := r.legalRuntime
	legalDependencies := r.legalDependencies
	formDependencies := r.formDependencies
	ogDeps := deps.og
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
	)
	emailCourierPath, emailCourierHandler := intrav1connect.NewEmailCourierServiceHandler(emailCourierService, internalHandlerOpts...)
	mux.Handle(emailCourierPath, internalRPCTrust.identity(emailCourierHandler))
	slog.Info("Registered internal service", "path", emailCourierPath)
	internalPageService := page.NewInternalPageService(
		db,
		servicePublisher,
		spicedbClient,
		pageRuntime,
		page.WithInternalPageDomainAuditWriter(telemetryWriter),
		page.WithInternalPageContentBlockStore(contentBlockStore),
		page.WithInternalPageContentBlockMediaHydrator(fileService),
	)
	internalPagePath, internalPageHandler := intrav1connect.NewInternalPageServiceHandler(internalPageService, internalHandlerOpts...)
	mux.Handle(internalPagePath, internalRPCTrust.collab(internalPageHandler))
	slog.Info("Registered internal service", "path", internalPagePath)

	internalPostService := post.NewInternalPostService(
		db,
		spicedbClient,
		servicePublisher,
		cfg.CDNURL,
		ogDeps.refresher,
		postMedia,
		post.WithInternalPostDomainAuditWriter(telemetryWriter),
		post.WithInternalPostContentBlockStore(contentBlockStore),
	)
	internalPostPath, internalPostHandler := intrav1connect.NewInternalPostServiceHandler(internalPostService, internalHandlerOpts...)
	mux.Handle(internalPostPath, internalRPCTrust.collab(internalPostHandler))
	slog.Info("Registered internal service", "path", internalPostPath)

	internalProgramEventService := programevent.NewAuditedInternalProgramEventService(
		db,
		servicePublisher,
		telemetryWriter,
		programevent.WithInternalProgramEventSpiceDB(spicedbClient),
		programevent.WithInternalProgramEventCheckpoints(collaborationRuntime.Checkpoints),
		programevent.WithInternalProgramEventContentBlockStore(contentBlockStore),
		programevent.WithInternalProgramEventMediaHydrator(programEventFiles),
	)
	internalProgramEventPath, internalProgramEventHandler := intrav1connect.NewInternalProgramEventServiceHandler(internalProgramEventService, internalHandlerOpts...)
	mux.Handle(internalProgramEventPath, internalRPCTrust.collab(internalProgramEventHandler))
	slog.Info("Registered internal service", "path", internalProgramEventPath)

	internalWorkService := work.NewInternalWorkService(
		db,
		servicePublisher,
		workRuntime,
		spicedbClient,
		work.WithInternalWorkDomainAuditWriter(telemetryWriter),
		work.WithInternalWorkCheckpoints(collaborationRuntime.Checkpoints),
		work.WithInternalWorkContentBlockStore(contentBlockStore),
		work.WithInternalWorkContentBlockMediaHydrator(fileService),
	)
	internalWorkPath, internalWorkHandler := intrav1connect.NewInternalWorkServiceHandler(internalWorkService, internalHandlerOpts...)
	mux.Handle(internalWorkPath, internalRPCTrust.collab(internalWorkHandler))
	slog.Info("Registered internal service", "path", internalWorkPath)

	internalMapService := maptheme.NewAuditedInternalMapService(db, telemetryWriter, spicedbClient)
	internalMapPath, internalMapHandler := intrav1connect.NewInternalMapServiceHandler(internalMapService, internalHandlerOpts...)
	mux.Handle(internalMapPath, internalRPCTrust.collab(internalMapHandler))
	slog.Info("Registered internal service", "path", internalMapPath)

	internalCampaignService := campaign.NewAuditedInternalCampaignService(
		db,
		telemetryWriter,
		campaign.WithInternalCampaignContentBlockStore(contentBlockStore),
		campaign.WithInternalCampaignSpiceDB(spicedbClient),
		campaign.WithInternalCampaignCheckpoints(collaborationRuntime.Checkpoints),
	)
	internalCampaignPath, internalCampaignHandler := intrav1connect.NewInternalCampaignServiceHandler(internalCampaignService, internalHandlerOpts...)
	mux.Handle(internalCampaignPath, internalRPCTrust.collab(internalCampaignHandler))
	slog.Info("Registered internal service", "path", internalCampaignPath)

	internalEmailTemplateService := emailauthoring.NewAuditedInternalEmailTemplateService(
		db,
		telemetryWriter,
		spicedbClient,
		emailauthoring.WithInternalEmailTemplateCheckpoints(collaborationRuntime.Checkpoints),
		emailauthoring.WithInternalEmailTemplateContentBlockStore(contentBlockStore),
		emailauthoring.WithInternalEmailTemplateCampaignDeliveryReferences(emailAuthoringReferences),
	)
	internalEmailTemplatePath, internalEmailTemplateHandler := intrav1connect.NewInternalEmailTemplateServiceHandler(internalEmailTemplateService, internalHandlerOpts...)
	mux.Handle(internalEmailTemplatePath, internalRPCTrust.collab(internalEmailTemplateHandler))
	slog.Info("Registered internal service", "path", internalEmailTemplatePath)

	internalEmailLayoutService := emailauthoring.NewAuditedInternalEmailLayoutService(
		db,
		telemetryWriter,
		emailauthoring.WithInternalEmailLayoutCheckpoints(collaborationRuntime.Checkpoints),
		emailauthoring.WithInternalEmailLayoutCampaignDeliveryReferences(emailAuthoringReferences),
		emailauthoring.WithInternalEmailLayoutContentBlockStore(contentBlockStore),
	)
	internalEmailLayoutPath, internalEmailLayoutHandler := intrav1connect.NewInternalEmailLayoutServiceHandler(internalEmailLayoutService, internalHandlerOpts...)
	mux.Handle(internalEmailLayoutPath, internalRPCTrust.collab(internalEmailLayoutHandler))
	slog.Info("Registered internal service", "path", internalEmailLayoutPath)

	internalTermsService := legal.NewAuditedInternalTermsService(
		db,
		telemetryWriter,
		legalDependencies,
		legal.WithInternalTermsContentBlocks(contentBlockStore, spicedbClient, collaborationRuntime.Checkpoints),
	)
	internalTermsPath, internalTermsHandler := intrav1connect.NewInternalTermsServiceHandler(internalTermsService, internalHandlerOpts...)
	mux.Handle(internalTermsPath, internalRPCTrust.collab(internalTermsHandler))
	slog.Info("Registered internal service", "path", internalTermsPath)

	internalPrivacyService := legal.NewAuditedInternalPrivacyService(
		db,
		telemetryWriter,
		legalDependencies,
		legal.WithInternalPrivacyContentBlocks(contentBlockStore, spicedbClient, collaborationRuntime.Checkpoints),
	)
	internalPrivacyPath, internalPrivacyHandler := intrav1connect.NewInternalPrivacyServiceHandler(internalPrivacyService, internalHandlerOpts...)
	mux.Handle(internalPrivacyPath, internalRPCTrust.collab(internalPrivacyHandler))
	slog.Info("Registered internal service", "path", internalPrivacyPath)

	internalFormService := formdomain.NewAuditedInternalFormService(db, servicePublisher, telemetryWriter, spicedbClient, formDependencies)
	internalFormPath, internalFormHandler := intrav1connect.NewInternalFormServiceHandler(internalFormService, internalHandlerOpts...)
	mux.Handle(internalFormPath, internalRPCTrust.collab(internalFormHandler))
	slog.Info("Registered internal service", "path", internalFormPath)

	legalAIDocumentService, err := legal.NewAuditedAIDocumentService(
		db, contentBlockStore, spicedbClient, legalRuntime, telemetryWriter,
	)
	if err != nil {
		return internalContentServices{}, fmt.Errorf("initialize Legal AI document service: %w", err)
	}
	return internalContentServices{
		page:           internalPageService,
		work:           internalWorkService,
		emailTemplate:  internalEmailTemplateService,
		campaign:       internalCampaignService,
		form:           internalFormService,
		legalDocuments: legalAIDocumentService,
	}, nil
}
