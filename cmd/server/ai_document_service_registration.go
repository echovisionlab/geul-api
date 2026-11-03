package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	aidocumentadapter "github.com/echovisionlab/geul-api/internal/adapters/aidocument"
	sitesettingsadapter "github.com/echovisionlab/geul-api/internal/adapters/sitesettings"
	"github.com/echovisionlab/geul-api/internal/aieditor"
	"github.com/echovisionlab/geul-api/internal/artist"
	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	formdomain "github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/label"
	"github.com/echovisionlab/geul-api/internal/legal"
	llmproviders "github.com/echovisionlab/geul-api/internal/llm/providers"
	"github.com/echovisionlab/geul-api/internal/menu"
	"github.com/echovisionlab/geul-api/internal/page"
	"github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/programevent"
	"github.com/echovisionlab/geul-api/internal/release"
	"github.com/echovisionlab/geul-api/internal/series"
	translationapplication "github.com/echovisionlab/geul-api/internal/translation/application"
	"github.com/echovisionlab/geul-api/internal/work"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

type aiDocumentApplications struct {
	post           *post.Service
	page           *page.Service
	work           *work.Service
	programEvent   *programevent.Service
	release        *release.Service
	artist         *artist.Service
	label          *label.Service
	menu           *menu.Service
	emailAuthoring *emailauthoring.InternalService
	campaign       *campaign.InternalService
	form           *formdomain.Service
	legal          *legal.AIDocumentService
	series         *series.Service
}

func (documents aiDocumentApplications) registrations() (aiDocumentDomainRegistrations, error) {
	var err error
	aiDocumentRegistrations := aiDocumentDomainRegistrations{}
	if aiDocumentRegistrations.post, err = aidocumentadapter.NewPostRegistration(documents.post); err != nil {
		return aiDocumentDomainRegistrations{}, fmt.Errorf("register Post AI document domain: %w", err)
	}
	if aiDocumentRegistrations.page, err = aidocumentadapter.NewPageRegistration(documents.page); err != nil {
		return aiDocumentDomainRegistrations{}, fmt.Errorf("register Page AI document domain: %w", err)
	}
	if aiDocumentRegistrations.work, err = aidocumentadapter.NewWorkRegistration(documents.work); err != nil {
		return aiDocumentDomainRegistrations{}, fmt.Errorf("register Work AI document domain: %w", err)
	}
	if aiDocumentRegistrations.programEvent, err = aidocumentadapter.NewProgramEventRegistration(documents.programEvent); err != nil {
		return aiDocumentDomainRegistrations{}, fmt.Errorf("register Program Event AI document domain: %w", err)
	}
	if aiDocumentRegistrations.release, err = aidocumentadapter.NewReleaseRegistration(documents.release); err != nil {
		return aiDocumentDomainRegistrations{}, fmt.Errorf("register Release AI document domain: %w", err)
	}
	if aiDocumentRegistrations.artist, err = aidocumentadapter.NewArtistRegistration(documents.artist); err != nil {
		return aiDocumentDomainRegistrations{}, fmt.Errorf("register Artist AI document domain: %w", err)
	}
	if aiDocumentRegistrations.label, err = aidocumentadapter.NewLabelRegistration(documents.label); err != nil {
		return aiDocumentDomainRegistrations{}, fmt.Errorf("register Label AI document domain: %w", err)
	}
	if aiDocumentRegistrations.menu, err = aidocumentadapter.NewMenuRegistration(documents.menu); err != nil {
		return aiDocumentDomainRegistrations{}, fmt.Errorf("register Menu AI document domain: %w", err)
	}
	if aiDocumentRegistrations.emailTemplate, err = aidocumentadapter.NewEmailTemplateRegistration(documents.emailAuthoring); err != nil {
		return aiDocumentDomainRegistrations{}, fmt.Errorf("register Email Template AI document domain: %w", err)
	}
	if aiDocumentRegistrations.emailLayout, err = aidocumentadapter.NewEmailLayoutRegistration(documents.emailAuthoring); err != nil {
		return aiDocumentDomainRegistrations{}, fmt.Errorf("register Email Layout AI document domain: %w", err)
	}
	if aiDocumentRegistrations.campaign, err = aidocumentadapter.NewCampaignRegistration(documents.campaign); err != nil {
		return aiDocumentDomainRegistrations{}, fmt.Errorf("register Campaign AI document domain: %w", err)
	}
	if aiDocumentRegistrations.form, err = aidocumentadapter.NewFormRegistration(documents.form); err != nil {
		return aiDocumentDomainRegistrations{}, fmt.Errorf("register Form AI document domain: %w", err)
	}
	if aiDocumentRegistrations.privacy, err = aidocumentadapter.NewPrivacyRegistration(documents.legal); err != nil {
		return aiDocumentDomainRegistrations{}, fmt.Errorf("register Privacy AI document domain: %w", err)
	}
	if aiDocumentRegistrations.terms, err = aidocumentadapter.NewTermsRegistration(documents.legal); err != nil {
		return aiDocumentDomainRegistrations{}, fmt.Errorf("register Terms AI document domain: %w", err)
	}
	if aiDocumentRegistrations.postSeries, err = aidocumentadapter.NewPostSeriesRegistration(
		documents.series,
	); err != nil {
		return aiDocumentDomainRegistrations{}, fmt.Errorf("register Post Series AI document domain: %w", err)
	}
	return aiDocumentRegistrations, nil
}

func (r serviceRegistration) registerAIDocuments(documents aiDocumentApplications, references contentMCPApplications, translations *translationapplication.Service, files *filemedia.Service) error {
	registrations, err := documents.registrations()
	if err != nil {
		return err
	}
	references.emailLayouts = documents.emailAuthoring.LayoutAIDocuments()
	aiDocumentMCP, err := newAIDocumentMCPComposition(
		registrations,
		documents.post,
		documents.work,
		documents.page,
		documents.programEvent,
		documents.release,
		documents.artist,
		references,
		translations,
		files,
		aiDocumentMCPConfig{
			internalServiceSecret:     r.dependencies.cfg.TokenSigningSecret,
			authHeaderName:            r.dependencies.cfg.AuthHeaderName,
			internalServiceHeaderName: r.dependencies.cfg.InternalServiceHeaderName,
			editorCollabURL:           r.dependencies.cfg.EditorCollabURL,
			editorCollabHTTPClient:    &http.Client{Timeout: time.Duration(r.dependencies.cfg.HTTPWriteTimeoutSec) * time.Second},
			allowedOrigins:            r.dependencies.cfg.CORSOrigins,
		},
		r.dependencies.servicePublisher,
		sitesettingsadapter.NewMCPServerTitleSource(r.dependencies.db),
	)
	if err != nil {
		return err
	}
	aiDocumentPath, aiDocumentHandler := managev1connect.NewAIDocumentServiceHandler(
		aiDocumentMCP.connectService,
		r.manageOptions...,
	)
	r.dependencies.mux.Handle(aiDocumentPath, aiDocumentHandler)
	slog.Info("Registered service", "path", aiDocumentPath)
	aiEditorProvider, err := llmproviders.NewGeminiProvider(llmproviders.GeminiConfig{APIKey: r.dependencies.cfg.GoogleAIAPIKey})
	if err != nil {
		return fmt.Errorf("initialize AI Editor Gemini provider: %w", err)
	}
	aiEditorService, err := aieditor.NewService(aiDocumentMCP.editorApplication, aiEditorProvider)
	if err != nil {
		return fmt.Errorf("initialize AI Editor service: %w", err)
	}
	aiEditorPath, aiEditorHandler := managev1connect.NewAIEditorOrchestrationServiceHandler(
		aiEditorService,
		r.manageOptions...,
	)
	r.dependencies.mux.Handle(aiEditorPath, aiEditorHandler)
	slog.Info("Registered service", "path", aiEditorPath)
	if err := registerMCPRoutes(r.dependencies.mux, aiDocumentMCP.mcpHandler); err != nil {
		return err
	}
	slog.Info("Registered MCP handler", "path", "/mcp", "methods", []string{http.MethodGet, http.MethodPost})

	return nil
}
