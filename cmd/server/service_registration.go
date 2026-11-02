package main

import (
	"fmt"
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
	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/sitesettings"
	"github.com/echovisionlab/geul-api/internal/telemetry"
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

	// Register Connect services
	// FileService first (needed by other services for file deletion)
	downloadTTL := time.Duration(deps.cfg.MediaDownloadTTLSec) * time.Second
	publicFileService, err := r.newPublicFiles(downloadTTL)
	if err != nil {
		return registeredServices{}, err
	}
	legalRuntime := legaladapter.NewOGRuntime(deps.cfg.CDNURL, deps.og.planner)
	campaignDeliveryDispatcher := campaignadapter.NewDeliveryDispatcher(deps.db, deps.spicedbClient, deps.workerPublisher, deps.telemetryWriter, legalRuntime)
	fileService, err := r.newFiles(downloadTTL, legalRuntime)
	if err != nil {
		return registeredServices{}, err
	}

	fileContentMedia := filemediacontent.New(fileService)
	formAssets := formadapter.NewAssets(deps.cfg.CDNURL)
	pageRuntime := pageadapter.NewRuntime(deps.cfg.CDNURL, deps.og.refresher)
	workRuntime := workadapter.NewRuntime(deps.db, deps.cfg.CDNURL, deps.og.refresher)
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

	music, err := r.registerMusicApplications(fileService, collaborationRuntime, downloadTTL)
	if err != nil {
		return registeredServices{}, err
	}

	if err := r.registerAI(); err != nil {
		return registeredServices{}, err
	}

	emailAuthoringReferences := emailauthoringadapter.NewCampaignDeliveryReferences()
	translationRegistry, err := translationadapter.NewDomainRegistry(emailAuthoringReferences, deps.telemetryWriter)
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize translation domain registry: %w", err)
	}
	r.registerAccountRoutes(accountComposition, accountHooks)

	content := contentApplicationRegistration{
		registration: r, publicFiles: publicFileService, files: fileService, media: fileContentMedia,
		workRuntime: workRuntime, pageRuntime: pageRuntime, collaboration: collaborationRuntime,
		formAssets: formAssets, references: music.references, legalRuntime: legalRuntime,
		campaignDelivery: campaignDeliveryDispatcher, emailAuthoringReferences: emailAuthoringReferences,
	}
	contentDocuments, err := content.registerDocuments()
	if err != nil {
		return registeredServices{}, err
	}

	contentSupport, err := content.registerSupport(contentDocuments.post)
	if err != nil {
		return registeredServices{}, err
	}

	internalServices, err := (internalContentServiceRegistration{
		dependencies:                deps,
		handlerOptions:              r.internalOptions,
		internalTrust:               r.internalTrust,
		accountEmailChangeLifecycle: accountHooks.emailChanges,
		programEvents:               contentDocuments.programEvent,
		emailAuthoringUnits:         contentSupport.emailAuthoringUnits,
		campaignUnits:               contentSupport.campaignUnits,
		legalUnits:                  contentSupport.legalUnits,
		mapThemes:                   contentSupport.mapTheme,
	}).register()
	if err != nil {
		return registeredServices{}, err
	}
	translationService, err := (translationServiceRegistration{
		dependencies:             deps,
		handlerOptions:           r.manageOptions,
		domainRegistry:           translationRegistry,
		emailAuthoringReferences: emailAuthoringReferences,
		legalDocuments:           internalServices.legalDocuments,
		seriesService:            contentSupport.series,
		fileService:              fileService,
	}).register()
	if err != nil {
		return registeredServices{}, err
	}
	if err := content.registerAIDocumentApplications(music, contentDocuments, contentSupport, internalServices, translationService, accountComposition.members); err != nil {
		return registeredServices{}, err
	}

	r.registerInternalOGRoutes()

	if err := content.registerPublic(accountComposition, contentDocuments.post); err != nil {
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

func (r serviceRegistration) registerSiteAndAdministration() (*sitesettings.Service, error) {
	if err := r.registerAudience(); err != nil {
		return nil, err
	}

	siteSettingService, err := r.registerSiteSettings()
	if err != nil {
		return nil, err
	}

	if err := r.registerAdmin(); err != nil {
		return nil, err
	}

	if err := r.registerShareLinks(); err != nil {
		return nil, err
	}

	return siteSettingService, nil
}
