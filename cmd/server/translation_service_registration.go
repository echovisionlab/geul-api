package main

import (
	"fmt"
	"log/slog"

	"connectrpc.com/connect"
	filemediaadapter "github.com/echovisionlab/geul-api/internal/adapters/filemedia"
	translationadapter "github.com/echovisionlab/geul-api/internal/adapters/translation"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	formdomain "github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/menu"
	"github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/programevent"
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
	emailAuthoringReferences emailauthoring.CampaignDeliveryReferences
	menuService              *menu.MenuService
	legalDocuments           *legal.AIDocumentService
	internalFormService      *formdomain.InternalFormService
	programEventService      *programevent.ProgramEventService
	seriesService            *series.SeriesService
	fileService              *filemedia.FileService
}

func (r translationServiceRegistration) register() (*translationapplication.TranslationService, error) {
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
	menuService := r.menuService
	legalAIDocumentService := r.legalDocuments
	internalFormService := r.internalFormService
	programEventService := r.programEventService
	seriesService := r.seriesService
	fileService := r.fileService
	translationInterchangeRegistrations := []translationadapter.InterchangeDomainRegistration{
		{
			Domain: translationcore.KindPage,
			Port: translationadapter.NewPageInterchangePort(
				telemetryWriter, sharedtelemetry.NewPageLocaleContentAuditRecord,
			),
		},
		{
			Domain: translationcore.KindPost,
			Port:   translationadapter.NewPostInterchange(post.NewTranslationInterchange(telemetryWriter)),
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
			Port:   translationadapter.NewMenuInterchange(menuService),
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
			Port:   translationadapter.NewLegalInterchange(legalAIDocumentService),
		},
		{
			Domain: translationcore.KindTerms,
			Port:   translationadapter.NewLegalInterchange(legalAIDocumentService),
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
			Port:   translationadapter.NewProgramEventInterchange(programEventService),
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
	translationService := translationapplication.NewAuditedTranslationService(
		db, workerPublisher, cfg.CDNURL, telemetryWriter, spicedbClient, ogDeps.planner, ogDeps.refresher,
		translationapplication.WithTranslationServiceContentBlockStore(contentBlockStore),
		translationapplication.WithTranslationServiceDomainRegistry(translationRegistry),
		translationapplication.WithTranslationServiceXLIFFFiles(translationXLIFFFiles),
		translationapplication.WithTranslationServiceInterchangeDomains(translationInterchangeRegistry),
	)
	translationPath, translationHandler := managev1connect.NewTranslationServiceHandler(translationService, handlerOpts...)
	mux.Handle(translationPath, translationHandler)
	slog.Info("Registered service", "path", translationPath)

	return translationService, nil
}
