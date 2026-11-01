package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	transcodepostgres "github.com/echovisionlab/geul-api/internal/transcode/postgres"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/account"
	campaignadapter "github.com/echovisionlab/geul-api/internal/adapters/campaign"
	emailauthoringadapter "github.com/echovisionlab/geul-api/internal/adapters/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/adapters/filemediacontent"
	formadapter "github.com/echovisionlab/geul-api/internal/adapters/form"
	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	pageadapter "github.com/echovisionlab/geul-api/internal/adapters/page"
	translationadapter "github.com/echovisionlab/geul-api/internal/adapters/translation"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authentication"
	"github.com/echovisionlab/geul-api/internal/config"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	"github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

type serviceRegistrationDependencies struct {
	mux                     *http.ServeMux
	cfg                     *config.Config
	db                      *gorm.DB
	s3Client                *s3.Client
	s3PresignClient         *s3.PresignClient
	servicePublisher        *mq.Publisher
	workerPublisher         *mq.Publisher
	hooksPublisher          *mq.Publisher
	ogPublisher             *mq.Publisher
	transcodeTracker        *transcodepostgres.Tracker
	spicedbClient           *auth.SpiceDBClient
	kratosClient            *auth.KratosClient
	passwordHasher          *crypto.PasswordHasher
	contentBlockStore       *contentblock.Store
	authCodeIssuanceLimiter *authentication.AuthCodeIssuanceLimiter
	adapterLoader           *email.AdapterLoader
	telemetryWriter         *telemetry.DurableWriter
	og                      *ogDependencies
}

type registeredServices struct {
	authInterceptor             *auth.AuthInterceptor
	accountEmailChangeLifecycle *account.AccountEmailChangeLifecycle
	accountLifecycle            *account.Service
	mcpAuthorAdmissionHandler   http.Handler
	oathkeeperPATHandler        http.Handler
}

type serviceRegistration struct {
	dependencies          serviceRegistrationDependencies
	manageOptions         []connect.HandlerOption
	internalOptions       []connect.HandlerOption
	publicOptions         []connect.HandlerOption
	internalTrust         internalRPCTrustBoundary
	authInterceptor       *auth.AuthInterceptor
	authenticationService *authentication.Service
}

func registerServices(deps serviceRegistrationDependencies) (registeredServices, error) {
	r, err := newServiceRegistration(deps)
	if err != nil {
		return registeredServices{}, err
	}
	mux := deps.mux
	cfg := deps.cfg
	db := deps.db
	workerPublisher := deps.workerPublisher
	spicedbClient := deps.spicedbClient
	telemetryWriter := deps.telemetryWriter
	ogDeps := deps.og

	// Register Connect services
	// FileService first (needed by other services for file deletion)
	downloadTTL := time.Duration(cfg.MediaDownloadTTLSec) * time.Second
	publicFileService, err := r.newPublicFiles(downloadTTL)
	if err != nil {
		return registeredServices{}, err
	}
	legalRuntime := legaladapter.NewOGRuntime(cfg.CDNURL, ogDeps.planner)
	campaignDeliveryDispatcher := campaignadapter.NewDeliveryDispatcher(db, spicedbClient, workerPublisher, telemetryWriter, legalRuntime)
	fileService, err := r.newFiles(downloadTTL, legalRuntime)
	if err != nil {
		return registeredServices{}, err
	}

	fileContentMedia := filemediacontent.New(fileService)
	formAssets := formadapter.NewAssets(cfg.CDNURL)
	pageRuntime := pageadapter.NewRuntime(cfg.CDNURL, ogDeps.refresher)
	workRuntime := workadapter.NewRuntime(db, cfg.CDNURL, ogDeps.refresher)
	r.registerFileRoutes(fileService)

	collaborationRuntime := r.registerCollaboration()

	accountComposition, err := r.registerAccounts(fileService)
	if err != nil {
		return registeredServices{}, err
	}

	accountHooks, err := r.registerAccountHooks(accountComposition)
	if err != nil {
		return registeredServices{}, err
	}

	r.registerEmailProviderCallbacks()

	r.registerUploadRoutes(fileService)

	music := musicServiceRegistration{
		dependencies: deps, files: fileService, checkpoints: collaborationRuntime.Checkpoints,
		manageOptions: r.manageOptions, internalOptions: r.internalOptions, publicOptions: r.publicOptions,
		internalTrust: r.internalTrust, downloadTTL: downloadTTL,
	}
	artistService, err := music.registerArtist()
	if err != nil {
		return registeredServices{}, err
	}
	labelService, err := music.registerLabel()
	if err != nil {
		return registeredServices{}, err
	}
	releaseService, err := music.registerRelease()
	if err != nil {
		return registeredServices{}, err
	}
	referenceCatalog, err := r.registerReferenceCatalog()
	if err != nil {
		return registeredServices{}, err
	}

	music.registerTaxonomy(referenceCatalog)

	if err := r.registerAI(); err != nil {
		return registeredServices{}, err
	}

	emailAuthoringReferences := emailauthoringadapter.NewCampaignDeliveryReferences()
	translationRegistry, err := translationadapter.NewDomainRegistry(emailAuthoringReferences, telemetryWriter)
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize translation domain registry: %w", err)
	}
	memberPath, memberHandler := managev1connect.NewMemberServiceHandler(member.NewConnectHandler(accountComposition.members), r.manageOptions...)
	mux.Handle(memberPath, memberHandler)
	slog.Info("Registered service", "path", memberPath)
	accountPath, accountHandler := managev1connect.NewAccountServiceHandler(accountHooks.account, r.manageOptions...)
	mux.Handle(accountPath, accountHandler)
	slog.Info("Registered service", "path", accountPath)

	workService, err := r.registerWork(fileContentMedia, workRuntime, collaborationRuntime)
	if err != nil {
		return registeredServices{}, err
	}

	// Phase 2: CMS Services
	postComposition, err := r.registerPost(publicFileService, fileService, fileContentMedia)
	if err != nil {
		return registeredServices{}, err
	}

	categoryPath, categoryHandler := managev1connect.NewCategoryServiceHandler(referencecatalog.NewCategoryConnectHandler(referenceCatalog), r.manageOptions...)
	mux.Handle(categoryPath, categoryHandler)
	slog.Info("Registered service", "path", categoryPath)

	programEventService, err := r.registerProgramEvent(fileContentMedia, collaborationRuntime)
	if err != nil {
		return registeredServices{}, err
	}

	tagPath, tagHandler := managev1connect.NewTagServiceHandler(referencecatalog.NewTagConnectHandler(referenceCatalog), r.manageOptions...)
	mux.Handle(tagPath, tagHandler)
	slog.Info("Registered service", "path", tagPath)

	pageService, err := r.registerPage(fileService, fileContentMedia, pageRuntime)
	if err != nil {
		return registeredServices{}, err
	}

	menuService, err := r.registerMenu(collaborationRuntime)
	if err != nil {
		return registeredServices{}, err
	}

	formService, err := r.registerForm(formAssets)
	if err != nil {
		return registeredServices{}, err
	}

	// Phase 3: Reference Data Services
	seriesService, err := r.registerSeries(collaborationRuntime)
	if err != nil {
		return registeredServices{}, err
	}

	clientPath, clientHandler := managev1connect.NewClientServiceHandler(referencecatalog.NewClientConnectHandler(referenceCatalog), r.manageOptions...)
	mux.Handle(clientPath, clientHandler)
	slog.Info("Registered service", "path", clientPath)

	legalUnits, legalPolicyService, err := r.registerLegal(legalRuntime, campaignDeliveryDispatcher, collaborationRuntime)
	if err != nil {
		return registeredServices{}, err
	}

	mapPlacePath, mapPlaceHandler := managev1connect.NewMapPlaceServiceHandler(referencecatalog.NewMapPlaceConnectHandler(referenceCatalog), r.manageOptions...)
	mux.Handle(mapPlacePath, mapPlaceHandler)
	slog.Info("Registered service", "path", mapPlacePath)

	mapThemeService, err := r.registerMapTheme()
	if err != nil {
		return registeredServices{}, err
	}

	emailAuthoringUnits, err := r.registerEmailAuthoring(collaborationRuntime, emailAuthoringReferences)
	if err != nil {
		return registeredServices{}, err
	}

	mailAdapterService, err := r.registerEmailDelivery()
	if err != nil {
		return registeredServices{}, err
	}

	if err := r.registerComments(postComposition.members, postComposition.units); err != nil {
		return registeredServices{}, err
	}

	campaignUnits, err := r.registerCampaign(collaborationRuntime)
	if err != nil {
		return registeredServices{}, err
	}

	if err := r.registerAudience(); err != nil {
		return registeredServices{}, err
	}

	siteSettingService, err := r.registerSiteSettings()
	if err != nil {
		return registeredServices{}, err
	}

	if err := r.registerAdmin(); err != nil {
		return registeredServices{}, err
	}

	if err := r.registerShareLinks(); err != nil {
		return registeredServices{}, err
	}

	internalServices, err := (internalContentServiceRegistration{
		dependencies:                deps,
		handlerOptions:              r.internalOptions,
		internalTrust:               r.internalTrust,
		accountEmailChangeLifecycle: accountHooks.emailChanges,
		programEvents:               programEventService,
		emailAuthoringUnits:         emailAuthoringUnits,
		campaignUnits:               campaignUnits,
		legalUnits:                  legalUnits,
		mapThemes:                   mapThemeService,
	}).register()
	if err != nil {
		return registeredServices{}, err
	}
	internalEmailAuthoringService := internalServices.emailAuthoring
	internalCampaignService := internalServices.campaign
	legalAIDocumentService := internalServices.legalDocuments
	translationService, err := (translationServiceRegistration{
		dependencies:             deps,
		handlerOptions:           r.manageOptions,
		domainRegistry:           translationRegistry,
		emailAuthoringReferences: emailAuthoringReferences,
		legalDocuments:           legalAIDocumentService,
		seriesService:            seriesService,
		fileService:              fileService,
	}).register()
	if err != nil {
		return registeredServices{}, err
	}
	if err := r.registerAIDocuments(aiDocumentApplications{
		post:           postComposition.service,
		page:           pageService,
		work:           workService,
		programEvent:   programEventService,
		release:        releaseService,
		artist:         artistService,
		label:          labelService,
		menu:           menuService,
		emailAuthoring: internalEmailAuthoringService,
		campaign:       internalCampaignService,
		form:           formService,
		legal:          legalAIDocumentService,
		series:         seriesService,
	}, contentMCPApplications{
		legalPolicies: legalPolicyService,
		categories:    referenceCatalog,
		tags:          referenceCatalog,
		clients:       referenceCatalog,
		mapPlaces:     referenceCatalog,
		members:       accountComposition.members,
		artists:       artistService,
		files:         fileService,
		eventTypes:    programEventService,
		eventSeries:   programEventService,
		labels:        labelService,
		genres:        referenceCatalog,
		styles:        referenceCatalog,
		formats:       referenceCatalog,
		forms:         formService,
		postSeries:    seriesService,
		tracks:        releaseService,
		mapThemes:     mapThemeService,
		menus:         menuService,
		menuSettings:  siteSettingService,
		siteSettings:  siteSettingService,
		mailAdapters:  mailAdapterService,
	}, translationService, fileService); err != nil {
		return registeredServices{}, err
	}

	internalOgService := og.NewInternalConnectHandler(og.NewLifecycle(ogDeps.units))
	internalOgPath, internalOgHandler := intrav1connect.NewInternalOgServiceHandler(internalOgService, r.internalOptions...)
	mux.Handle(internalOgPath, r.internalTrust.og(internalOgHandler))
	slog.Info("Registered internal service", "path", internalOgPath)
	if err := (publicServiceRegistration{
		dependencies:   deps,
		handlerOptions: r.publicOptions,
		fileService:    publicFileService,
		postShareLinks: postComposition.shareLinks,
		postMembers:    postComposition.members,
		workRuntime:    workRuntime,
		legalRuntime:   legalRuntime,
		formAssets:     formAssets,
		accounts:       accountComposition.account,
	}).register(); err != nil {
		return registeredServices{}, err
	}

	return registeredServices{
		authInterceptor:             r.authInterceptor,
		accountEmailChangeLifecycle: accountHooks.emailChanges,
		accountLifecycle:            accountComposition.account,
		mcpAuthorAdmissionHandler:   accountHooks.mcpAdmission,
		oathkeeperPATHandler:        accountHooks.oathkeeper,
	}, nil
}
