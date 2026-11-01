package main

import (
	"fmt"
	"log/slog"

	"connectrpc.com/connect"
	filemediaadapter "github.com/echovisionlab/geul-api/internal/adapters/filemedia"
	translationadapter "github.com/echovisionlab/geul-api/internal/adapters/translation"
	emailauthoringpostgres "github.com/echovisionlab/geul-api/internal/emailauthoring/postgres"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	formdomain "github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/legal"
	postpostgres "github.com/echovisionlab/geul-api/internal/post/postgres"
	"github.com/echovisionlab/geul-api/internal/series"
	translationcore "github.com/echovisionlab/geul-api/internal/translation"
	translationapplication "github.com/echovisionlab/geul-api/internal/translation/application"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// translationServiceRegistration assembles the translation ports after their
// domain services exist, then registers the authenticated Manage endpoint.
type translationServiceRegistration struct {
	dependencies             serviceRegistrationDependencies
	handlerOptions           []connect.HandlerOption
	domainRegistry           *translationadapter.DomainRegistry
	emailAuthoringReferences emailauthoringpostgres.DeliveryReferences
	legalDocuments           *legal.AIDocumentService
	internalFormService      *formdomain.Service
	seriesService            *series.Service
	fileService              *filemedia.Service
}

func (r translationServiceRegistration) register() (*translationapplication.Service, error) {
	deps := r.dependencies
	mux := deps.mux
	cfg := deps.cfg
	db := deps.db
	workerPublisher := deps.workerPublisher
	telemetryWriter := deps.telemetryWriter
	spicedbClient := deps.spicedbClient
	contentBlockStore := deps.contentBlockStore
	ogDeps := deps.og
	handlerOpts := r.handlerOptions
	translationRegistry := r.domainRegistry
	emailAuthoringReferences := r.emailAuthoringReferences
	legalAIDocumentService := r.legalDocuments
	internalFormService := r.internalFormService
	seriesService := r.seriesService
	fileService := r.fileService
	postInterchange, err := postpostgres.NewTranslationInterchange(telemetryWriter)
	if err != nil {
		return nil, fmt.Errorf("initialize Post translation interchange: %w", err)
	}
	translationInterchangeRegistrations := []translationadapter.InterchangeDomainRegistration{
		{
			Domain: translationcore.KindPage,
			Port: translationadapter.NewPageInterchangePort(
				telemetryWriter, sharedtelemetry.NewPageLocaleContentAuditRecord,
			),
		},
		{
			Domain: translationcore.KindPost,
			Port:   translationadapter.NewPostInterchange(postInterchange),
		},
		{
			Domain: translationcore.KindWork,
			Port: translationadapter.NewWorkInterchangePort(
				telemetryWriter, sharedtelemetry.NewWorkLocaleContentAuditRecord,
			),
		},
		{
			Domain: translationcore.KindRelease,
			Port: translationadapter.NewReleaseInterchange(
				telemetryWriter, sharedtelemetry.NewReleaseLocaleContentAuditRecord,
			),
		},
		{
			Domain: translationcore.KindArtist,
			Port: translationadapter.NewArtistInterchange(
				telemetryWriter, sharedtelemetry.NewArtistLocaleContentAuditRecord,
			),
		},
		{
			Domain: translationcore.KindLabel,
			Port:   translationadapter.NewLabelInterchange(telemetryWriter),
		},
		{
			Domain: translationcore.KindMenu,
			Port:   translationadapter.NewMenuInterchange(telemetryWriter),
		},
		{
			Domain: translationcore.KindEmailTemplate,
			Port: translationadapter.NewEmailTemplateInterchange(
				emailAuthoringReferences, telemetryWriter, sharedtelemetry.NewEmailTemplateLocaleContentAuditRecord,
			),
		},
		{
			Domain: translationcore.KindEmailLayout,
			Port: translationadapter.NewEmailLayoutInterchange(
				emailAuthoringReferences, telemetryWriter, sharedtelemetry.NewEmailLayoutLocaleContentAuditRecord,
			),
		},
		{
			Domain: translationcore.KindPrivacy,
			Port:   translationadapter.NewLegalInterchange(legalAIDocumentService, telemetryWriter),
		},
		{
			Domain: translationcore.KindTerms,
			Port:   translationadapter.NewLegalInterchange(legalAIDocumentService, telemetryWriter),
		},
		{
			Domain: translationcore.KindCampaign,
			Port: translationadapter.NewCampaignInterchange(
				telemetryWriter, sharedtelemetry.NewCampaignLocaleContentAuditRecord,
			),
		},
		{
			Domain: translationcore.KindForm,
			Port:   translationadapter.NewFormInterchange(internalFormService),
		},
		{
			Domain: translationcore.KindProgramEvent,
			Port:   translationadapter.NewProgramEventInterchange(telemetryWriter),
		},
		{
			Domain: translationcore.KindPostSeries,
			Port:   translationadapter.NewPostSeriesInterchange(seriesService),
		},
	}
	translationInterchangeRegistry, err := translationadapter.NewInterchangeRegistry(
		translationInterchangeRegistrations...,
	)
	if err != nil {
		return nil, fmt.Errorf("initialize Translation interchange registry: %w", err)
	}
	translationXLIFFFiles, err := filemediaadapter.NewTranslationXLIFFFiles(fileService)
	if err != nil {
		return nil, fmt.Errorf("initialize Translation XLIFF File runtime: %w", err)
	}
	translationService, err := translationapplication.NewService(translationapplication.Deps{
		DB: db, SpiceDB: spicedbClient, Audit: telemetryWriter, Publisher: workerPublisher,
		ContentBlocks: contentBlockStore, OGPlanner: ogDeps.planner, OGRefresher: ogDeps.refresher,
		Domains: translationRegistry, XLIFFFiles: translationXLIFFFiles,
		InterchangeDomains: translationInterchangeRegistry, CDNDomain: cfg.CDNURL,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize Translation service: %w", err)
	}
	translationPath, translationHandler := managev1connect.NewTranslationServiceHandler(
		translationapplication.NewConnectHandler(translationService), handlerOpts...,
	)
	mux.Handle(translationPath, translationHandler)
	slog.Info("Registered service", "path", translationPath)

	return translationService, nil
}
