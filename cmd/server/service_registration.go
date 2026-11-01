package main

import (
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/echovisionlab/geul-api/internal/account"
	accountadapter "github.com/echovisionlab/geul-api/internal/adapters/account"
	aidocumentadapter "github.com/echovisionlab/geul-api/internal/adapters/aidocument"
	artistadapter "github.com/echovisionlab/geul-api/internal/adapters/artist"
	audienceadapter "github.com/echovisionlab/geul-api/internal/adapters/audience"
	authenticationadapter "github.com/echovisionlab/geul-api/internal/adapters/authentication"
	campaignadapter "github.com/echovisionlab/geul-api/internal/adapters/campaign"
	collaborationadapter "github.com/echovisionlab/geul-api/internal/adapters/collaboration"
	emailauthoringadapter "github.com/echovisionlab/geul-api/internal/adapters/emailauthoring"
	emaildeliveryadapter "github.com/echovisionlab/geul-api/internal/adapters/emaildelivery"
	filemediaadapter "github.com/echovisionlab/geul-api/internal/adapters/filemedia"
	formadapter "github.com/echovisionlab/geul-api/internal/adapters/form"
	formogadapter "github.com/echovisionlab/geul-api/internal/adapters/form/og"
	labeladapter "github.com/echovisionlab/geul-api/internal/adapters/label"
	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	mediaassetadapter "github.com/echovisionlab/geul-api/internal/adapters/mediaasset"
	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	memberpatadapter "github.com/echovisionlab/geul-api/internal/adapters/memberpat"
	menuadapter "github.com/echovisionlab/geul-api/internal/adapters/menu"
	ogadapter "github.com/echovisionlab/geul-api/internal/adapters/og"
	pageadapter "github.com/echovisionlab/geul-api/internal/adapters/page"
	pageruntime "github.com/echovisionlab/geul-api/internal/adapters/page/runtime"
	"github.com/echovisionlab/geul-api/internal/adapters/patauth"
	postadapter "github.com/echovisionlab/geul-api/internal/adapters/post"
	postruntime "github.com/echovisionlab/geul-api/internal/adapters/post/runtime"
	programeventadapter "github.com/echovisionlab/geul-api/internal/adapters/programevent"
	referencecatalogadapter "github.com/echovisionlab/geul-api/internal/adapters/referencecatalog"
	referencecatalogmenuadapter "github.com/echovisionlab/geul-api/internal/adapters/referencecatalog/menu"
	releaseadapter "github.com/echovisionlab/geul-api/internal/adapters/release"
	seriesadapter "github.com/echovisionlab/geul-api/internal/adapters/series"
	sharelinkadapter "github.com/echovisionlab/geul-api/internal/adapters/sharelink"
	sitesettingsadapter "github.com/echovisionlab/geul-api/internal/adapters/sitesettings"
	translationadapter "github.com/echovisionlab/geul-api/internal/adapters/translation"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/admin"
	adminpostgres "github.com/echovisionlab/geul-api/internal/admin/postgres"
	"github.com/echovisionlab/geul-api/internal/ai"
	"github.com/echovisionlab/geul-api/internal/aieditor"
	"github.com/echovisionlab/geul-api/internal/audience"
	audiencepostgres "github.com/echovisionlab/geul-api/internal/audience/postgres"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authentication"
	authenticationpostgres "github.com/echovisionlab/geul-api/internal/authentication/postgres"
	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/collaboration"
	"github.com/echovisionlab/geul-api/internal/config"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	emaildeliverypostgres "github.com/echovisionlab/geul-api/internal/emaildelivery/postgres"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	filepublic "github.com/echovisionlab/geul-api/internal/filemedia/public"
	formdomain "github.com/echovisionlab/geul-api/internal/form"
	formpostgres "github.com/echovisionlab/geul-api/internal/form/postgres"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/legal"
	legalpostgres "github.com/echovisionlab/geul-api/internal/legal/postgres"
	llmproviders "github.com/echovisionlab/geul-api/internal/llm/providers"
	"github.com/echovisionlab/geul-api/internal/maptheme"
	mapthemepostgres "github.com/echovisionlab/geul-api/internal/maptheme/postgres"
	"github.com/echovisionlab/geul-api/internal/member"
	memberpat "github.com/echovisionlab/geul-api/internal/member/pat"
	"github.com/echovisionlab/geul-api/internal/menu"
	menupostgres "github.com/echovisionlab/geul-api/internal/menu/postgres"
	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/page"
	pagepostgres "github.com/echovisionlab/geul-api/internal/page/postgres"
	"github.com/echovisionlab/geul-api/internal/post"
	postpostgres "github.com/echovisionlab/geul-api/internal/post/postgres"
	"github.com/echovisionlab/geul-api/internal/programevent"
	programeventpostgres "github.com/echovisionlab/geul-api/internal/programevent/postgres"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	referencecatalogpostgres "github.com/echovisionlab/geul-api/internal/referencecatalog/postgres"
	"github.com/echovisionlab/geul-api/internal/series"
	seriespostgres "github.com/echovisionlab/geul-api/internal/series/postgres"
	"github.com/echovisionlab/geul-api/internal/sharelink"
	sharelinkpostgres "github.com/echovisionlab/geul-api/internal/sharelink/postgres"
	"github.com/echovisionlab/geul-api/internal/sitesettings"
	sitesettingspostgres "github.com/echovisionlab/geul-api/internal/sitesettings/postgres"
	"github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/transcode"
	"github.com/echovisionlab/geul-api/internal/work"
	workpostgres "github.com/echovisionlab/geul-api/internal/work/postgres"
	"github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	"go.akshayshah.org/connectproto"
	"google.golang.org/protobuf/encoding/protojson"
	"gorm.io/gorm"
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
	transcodeTracker        *transcode.Tracker
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

type personalAccessTokenComposition struct {
	tokens         *memberpat.Service
	accountHandler *accountadapter.PersonalAccessTokenHandler
}

func newPersonalAccessTokenComposition(
	delegate managev1connect.AccountServiceHandler,
	repository memberpat.Repository,
	clock memberpat.Clock,
	random io.Reader,
) (personalAccessTokenComposition, error) {
	tokens, err := memberpat.NewService(repository, clock, random)
	if err != nil {
		return personalAccessTokenComposition{}, fmt.Errorf("initialize personal access token service: %w", err)
	}
	accountHandler, err := accountadapter.NewPersonalAccessTokenHandler(delegate, tokens)
	if err != nil {
		return personalAccessTokenComposition{}, fmt.Errorf("initialize Account personal access token handler: %w", err)
	}
	return personalAccessTokenComposition{
		tokens: tokens, accountHandler: accountHandler,
	}, nil
}

func registerServices(deps serviceRegistrationDependencies) (registeredServices, error) {
	mux := deps.mux
	cfg := deps.cfg
	db := deps.db
	s3Client := deps.s3Client
	s3PresignClient := deps.s3PresignClient
	servicePublisher := deps.servicePublisher
	workerPublisher := deps.workerPublisher
	hooksPublisher := deps.hooksPublisher
	ogPublisher := deps.ogPublisher
	transcodeTracker := deps.transcodeTracker
	spicedbClient := deps.spicedbClient
	kratosClient := deps.kratosClient
	passwordHasher := deps.passwordHasher
	contentBlockStore := deps.contentBlockStore
	adapterLoader := deps.adapterLoader
	telemetryWriter := deps.telemetryWriter
	ogDeps := deps.og

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
		return registeredServices{}, fmt.Errorf("initialize authentication service: %w", err)
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

	// Register Connect services
	// FileService first (needed by other services for file deletion)
	downloadTTL := time.Duration(cfg.MediaDownloadTTLSec) * time.Second
	publicFileService, err := filepublic.NewService(filepublic.Deps{
		DB: db, SpiceDB: spicedbClient, Segments: mediaassetadapter.NewSegmentConfigs(),
		CDNDomain: cfg.CDNURL, MediaDomain: cfg.MediaURL, MediaSecret: cfg.MediaSigningSecret, DownloadTTL: downloadTTL,
	})
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize public file service: %w", err)
	}
	legalRuntime := legaladapter.NewOGRuntime(cfg.CDNURL, ogDeps.planner)
	campaignDeliveryDispatcher := campaignadapter.NewDeliveryDispatcher(db, campaign.NewAuditedCampaignDeliveryDispatcher(
		spicedbClient,
		workerPublisher,
		telemetryWriter,
		campaign.WithLegalNoticeDeliveryPort(legalRuntime),
	))
	fileService, err := filemedia.NewService(filemedia.Deps{
		DB: db, SpiceDB: spicedbClient, Audit: telemetryWriter, S3: s3Client, Presigner: s3PresignClient,
		AsyncPublisher: servicePublisher, Transcoder: transcodeTracker, S3Bucket: cfg.S3Bucket,
		CDNDomain: cfg.CDNURL, MediaDomain: cfg.MediaURL, MediaSecret: cfg.MediaSigningSecret,
		DownloadTTL: downloadTTL, EditorImageMaxSize: cfg.EditorImageMaxSize, LegalRoutes: legalRuntime,
		PostAccess:             filemediaadapter.NewPostAccess(db, spicedbClient),
		PagePolicyAccess:       pageadapter.NewPolicyAccess(page.NewPolicyAuthority(spicedbClient)),
		WorkAttachment:         filemediaadapter.NewWorkAttachment(db),
		WorkPolicyAccess:       workadapter.NewPolicyAccess(spicedbClient),
		ProgramEventAttachment: filemediaadapter.NewProgramEventAttachment(db),
		TrackAttachment:        filemediaadapter.NewTrackAttachment(telemetryWriter),
		ReleasePolicyAccess:    releaseadapter.NewPolicyAccess(spicedbClient),
		AudienceAccess:         filemediaadapter.NewAudienceAccess(),
		MemberSummaries:        filemediaadapter.NewMemberSummaries(db, cfg.CDNURL),
	})
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize file service: %w", err)
	}
	formAssets := formadapter.NewAssets(cfg.CDNURL)
	pageRuntime := pageadapter.NewRuntime(cfg.CDNURL, ogDeps.refresher)
	workRuntime := workadapter.NewRuntime(db, cfg.CDNURL, ogDeps.refresher)
	filePath, fileHandler := managev1connect.NewFileServiceHandler(filemedia.NewConnectHandler(fileService), handlerOpts...)
	mux.Handle(filePath, fileHandler)
	slog.Info("Registered service", "path", filePath)
	linkPreviewPath, linkPreviewHandler := managev1connect.NewLinkPreviewServiceHandler(
		filemedia.NewLinkPreviewConnectHandler(filemedia.NewLinkPreviewService(spicedbClient)),
		handlerOpts...,
	)
	mux.Handle(linkPreviewPath, linkPreviewHandler)
	slog.Info("Registered service", "path", linkPreviewPath)
	internalFileIngestService := filemedia.NewInternalConnectHandler(fileService)
	internalFileIngestPath, internalFileIngestHandler := intrav1connect.NewInternalFileIngestServiceHandler(
		internalFileIngestService,
		internalHandlerOpts...,
	)
	mux.Handle(internalFileIngestPath, internalRPCTrust.collab(internalFileIngestHandler))
	slog.Info("Registered internal service", "path", internalFileIngestPath)
	collaborationRuntime := collaborationadapter.NewRuntime(db, spicedbClient, cfg.CDNURL)
	internalCollaborationAuthorizationService := collaboration.NewService(
		db,
		collaborationRuntime.Registry,
		collaborationRuntime.Members,
	)
	internalCollaborationAuthorizationPath, internalCollaborationAuthorizationHandler := intrav1connect.NewInternalCollaborationAuthorizationServiceHandler(
		internalCollaborationAuthorizationService,
		internalHandlerOpts...,
	)
	mux.Handle(internalCollaborationAuthorizationPath, internalRPCTrust.collab(internalCollaborationAuthorizationHandler))
	slog.Info("Registered internal service", "path", internalCollaborationAuthorizationPath)

	memberService, err := member.NewService(member.Deps{
		DB:            db,
		SpiceDB:       spicedbClient,
		Identity:      kratosClient,
		AvatarFiles:   fileService,
		WelcomeEmails: servicePublisher,
		Accounts:      memberadapter.AccountSummaryReader{},
		AccountEmails: memberadapter.AccountEmailProjection{},
		Audit:         telemetryWriter,
		CDNDomain:     cfg.CDNURL,
		SiteOrigin:    cfg.SiteOrigin,
	})
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize member service: %w", err)
	}
	accounts, err := account.NewService(account.Deps{
		DB:             db,
		SpiceDB:        spicedbClient,
		Identity:       kratosClient,
		Publisher:      workerPublisher,
		Newsletter:     accountadapter.NewsletterSubscription{},
		MemberDeletion: accountadapter.MemberDeletion{},
		MemberEmails:   accountadapter.MemberEmailProjection{},
		Audit:          telemetryWriter,
		SiteOrigin:     cfg.SiteOrigin,
	})
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize account service: %w", err)
	}
	personalAccessTokenRepository, err := memberpatadapter.NewPersonalAccessTokenRepository(db)
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize personal access token repository: %w", err)
	}
	personalAccessTokenHandlers, err := newPersonalAccessTokenComposition(
		account.NewConnectHandler(accounts),
		personalAccessTokenRepository,
		memberpatadapter.SystemClock{},
		rand.Reader,
	)
	if err != nil {
		return registeredServices{}, err
	}
	if err := registerPATVerification(mux, cfg.PATVerificationSecret, personalAccessTokenHandlers.tokens); err != nil { //nolint:staticcheck // legacy Worker verifier credential, retained for migration
		return registeredServices{}, fmt.Errorf("initialize PAT verification: %w", err)
	}
	oathkeeperPATHandler, err := patauth.NewOathkeeper(cfg.TokenSigningSecret, cfg.InternalServiceHeaderName, personalAccessTokenHandlers.tokens)
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize Oathkeeper PAT verification: %w", err)
	}
	accountService := personalAccessTokenHandlers.accountHandler
	mcpAuthorAdmissionHandler, err := authentication.NewMCPGatewayAuthorAdmissionHandler(
		cfg.TokenSigningSecret,
		cfg.AuthHeaderName,
		cfg.InternalServiceHeaderName,
		spicedbClient,
	)
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize MCP gateway author admission: %w", err)
	}

	accountEmailChangeLifecycle := account.NewAccountEmailChangeLifecycle(
		db,
		kratosClient,
		hooksPublisher,
		accountadapter.MemberEmailProjection{},
	)
	directRoles := accountadapter.AccountDirectRoleTransition{}
	registrationMembers := member.NewMemberProvisioner(
		db,
		kratosClient,
		spicedbClient,
		memberadapter.AccountEmailProjection{},
		directRoles,
	)
	loginHooks := authentication.NewLoginHookService(
		kratosClient,
		authenticationadapter.NewLoginMemberProvisioner(registrationMembers),
		authenticationService,
	)
	registrationHooks := authentication.NewRegistrationHookPolicy(
		authenticationadapter.NewRegistrationReuseHoldChecker(db),
	)
	accountSettingsHooks := account.NewAccountSettingsHookService(kratosClient, accountEmailChangeLifecycle)
	credentialHooks := account.NewAccountCredentialHookLifecycle(
		db,
		kratosClient,
		telemetryWriter,
		hooksPublisher,
		accountadapter.MemberEmailProjection{},
	)
	// Hooks handler (for Kratos webhooks)
	authenticationHooks := authenticationadapter.NewHooksHandler(loginHooks, registrationHooks)
	accountHooks := accountadapter.NewSettingsHooksHandler(accountSettingsHooks, credentialHooks)
	protectInternalHook := func(h http.HandlerFunc) http.Handler {
		return internalRPCTrust.identity(h)
	}
	authenticationHooks.RegisterRoutes(mux, protectInternalHook)
	accountHooks.RegisterRoutes(mux, protectInternalHook)

	if strings.TrimSpace(cfg.SESEventSNSTopicARN) != "" {
		providerNotifications := emaildelivery.NewProviderNotificationProcessor(
			emaildeliveryadapter.NewCampaignProviderOutcomeStore(db),
			emaildeliveryadapter.NewSuppressionStore(db),
		)
		sesEventHandler := emaildeliveryadapter.NewSESEventHandler(cfg.SESEventSNSTopicARN, providerNotifications)
		mux.Handle(emaildeliveryadapter.SESEventCallbackPath, sesEventHandler)
		slog.Info("Registered SES event callback", "path", emaildeliveryadapter.SESEventCallbackPath)
	}

	// Authenticated multipart upload plane. Managed public-asset bytes are
	// relayed through the API; approved editor and large-media types use the
	// short-lived public S3 presigned control endpoints.
	mux.Handle("/upload/media-artifact", auth.RequireGatewaySession(db, filemedia.WithUploadIdleTimeout(http.HandlerFunc(fileService.HandleClientMediaArtifact))))
	mux.Handle("/upload/source", auth.RequireGatewaySession(db, filemedia.WithUploadIdleTimeout(http.HandlerFunc(fileService.HandleUploadSource))))
	mux.Handle(
		"/upload/part",
		auth.RequireGatewaySession(db, filemedia.WithUploadIdleTimeout(http.HandlerFunc(fileService.HandleUploadPart))),
	)
	mux.Handle(
		"/upload/prefix",
		auth.RequireGatewaySession(db, filemedia.WithUploadIdleTimeout(http.HandlerFunc(fileService.HandleVerifyUploadPrefix))),
	)
	mux.Handle(
		"/upload/part/presign",
		auth.RequireGatewaySession(db, http.HandlerFunc(fileService.HandlePresignUploadPart)),
	)
	mux.Handle(
		"/upload/part/confirm",
		auth.RequireGatewaySession(db, http.HandlerFunc(fileService.HandleConfirmUploadPart)),
	)
	slog.Info("Registered handlers", "paths", []string{"/upload/source", "/upload/part", "/upload/prefix", "/upload/part/presign", "/upload/part/confirm"})

	music := musicServiceRegistration{
		dependencies: deps, files: fileService, checkpoints: collaborationRuntime.Checkpoints,
		manageOptions: handlerOpts, internalOptions: internalHandlerOpts, publicOptions: publicHandlerOpts,
		internalTrust: internalRPCTrust, downloadTTL: downloadTTL,
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
	referenceCatalogUnits, err := referencecatalogpostgres.NewUnits(db, referencecatalogpostgres.Ports{
		Assets:      referencecatalogadapter.NewAssets(cfg.CDNURL),
		Members:     referencecatalogadapter.NewMemberSummaries(cfg.CDNURL),
		MenuTargets: referencecatalogmenuadapter.NewTargets(telemetryWriter),
		ClientWorks: workadapter.ClientWorks{},
	})
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize reference catalog store: %w", err)
	}
	referenceCatalog, err := referencecatalog.NewService(referencecatalog.Deps{
		Units: referenceCatalogUnits, SpiceDB: spicedbClient, Fence: identitystate.PostgresFence{}, Audit: telemetryWriter,
	})
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize reference catalog service: %w", err)
	}
	music.registerTaxonomy(referenceCatalog)

	aiService, err := ai.NewService(ai.Deps{
		DB: db, SpiceDB: spicedbClient, Audit: telemetryWriter, Publisher: servicePublisher,
	})
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize AI service: %w", err)
	}
	aiPath, aiHandler := managev1connect.NewAIServiceHandler(ai.NewConnectHandler(aiService), handlerOpts...)
	mux.Handle(aiPath, aiHandler)
	slog.Info("Registered service", "path", aiPath)

	emailAuthoringReferences := emailauthoringadapter.NewCampaignDeliveryReferences()
	translationRegistry, err := translationadapter.NewDomainRegistry(emailAuthoringReferences, telemetryWriter)
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize translation domain registry: %w", err)
	}
	memberPath, memberHandler := managev1connect.NewMemberServiceHandler(member.NewConnectHandler(memberService), handlerOpts...)
	mux.Handle(memberPath, memberHandler)
	slog.Info("Registered service", "path", memberPath)
	accountPath, accountHandler := managev1connect.NewAccountServiceHandler(accountService, handlerOpts...)
	mux.Handle(accountPath, accountHandler)
	slog.Info("Registered service", "path", accountPath)

	workUnits, err := workpostgres.NewUnits(db, workpostgres.Ports{
		ContentBlocks: contentBlockStore, Signals: servicePublisher, Checkpoints: collaborationRuntime.Checkpoints,
		Runtime: workRuntime, Media: fileService, ShareLinks: sharelinkadapter.EntityLinks{Type: sharelink.EntityWork},
	})
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize Work store: %w", err)
	}
	workService, err := work.NewService(work.Deps{
		Units: workUnits, SpiceDB: spicedbClient, Fence: identitystate.PostgresFence{}, Audit: telemetryWriter,
		Members: workadapter.NewMemberSummaries(db, cfg.CDNURL),
	})
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize Work service: %w", err)
	}
	workPath, workHandler := managev1connect.NewWorkServiceHandler(work.NewConnectHandler(workService), handlerOpts...)
	mux.Handle(workPath, workHandler)
	slog.Info("Registered service", "path", workPath)
	internalWorkPath, internalWorkHandler := intrav1connect.NewInternalWorkServiceHandler(work.NewInternalConnectHandler(workService), internalHandlerOpts...)
	mux.Handle(internalWorkPath, internalRPCTrust.collab(internalWorkHandler))
	slog.Info("Registered internal service", "path", internalWorkPath)

	// Phase 2: CMS Services
	postMembers := postadapter.NewMemberSummaries(db, cfg.CDNURL)
	postShareLinks := postruntime.ShareLinks{}
	postMedia := postruntime.ContentBlockMedia{}
	postFiles := postadapter.NewFiles(fileService, publicFileService)
	postVersionRestore := postruntime.VersionRestore{}
	postUnits, err := postpostgres.NewUnits(db, postpostgres.Deps{
		ContentBlocks: contentBlockStore, Signals: servicePublisher, OG: ogDeps.refresher,
		ShareLinkCleanup: sharelinkadapter.EntityLinks{Type: sharelink.EntityPost},
		MediaLoader:      postMedia, Media: fileService, VersionRestore: postVersionRestore, CDNDomain: cfg.CDNURL,
	})
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize Post store: %w", err)
	}
	postService, err := post.NewService(post.Deps{
		Units: postUnits, SpiceDB: spicedbClient, Fence: identitystate.PostgresFence{}, Audit: telemetryWriter,
		Files: postFiles, Members: postMembers,
	})
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize Post service: %w", err)
	}
	postPath, postHandler := managev1connect.NewPostServiceHandler(post.NewConnectHandler(postService), handlerOpts...)
	mux.Handle(postPath, postHandler)
	slog.Info("Registered service", "path", postPath)
	internalPostPath, internalPostHandler := intrav1connect.NewInternalPostServiceHandler(post.NewInternalConnectHandler(postService), internalHandlerOpts...)
	mux.Handle(internalPostPath, internalRPCTrust.collab(internalPostHandler))
	slog.Info("Registered internal service", "path", internalPostPath)

	categoryPath, categoryHandler := managev1connect.NewCategoryServiceHandler(referencecatalog.NewCategoryConnectHandler(referenceCatalog), handlerOpts...)
	mux.Handle(categoryPath, categoryHandler)
	slog.Info("Registered service", "path", categoryPath)

	programEventUnits, err := programeventpostgres.NewUnits(db, programeventpostgres.Ports{
		ContentBlocks: contentBlockStore, Signals: servicePublisher, Media: programeventadapter.NewRuntime(cfg.CDNURL),
		Files: programeventadapter.NewFiles(fileService), Members: memberadapter.ContentMembers{},
		CreditArtists: artistadapter.CreditArtists{}, Checkpoints: collaborationRuntime.Checkpoints,
	})
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize program event store: %w", err)
	}
	programEventService, err := programevent.NewService(programevent.Deps{
		Units: programEventUnits, SpiceDB: spicedbClient, Fence: identitystate.PostgresFence{}, Audit: telemetryWriter,
		CreditMembers: programeventadapter.NewCreditMemberSummaries(db, cfg.CDNURL),
	})
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize program event service: %w", err)
	}
	programEventTypePath, programEventTypeHandler := managev1connect.NewProgramEventTypeServiceHandler(programevent.NewTypeConnectHandler(programEventService), handlerOpts...)
	mux.Handle(programEventTypePath, programEventTypeHandler)
	slog.Info("Registered service", "path", programEventTypePath)

	programEventSeriesPath, programEventSeriesHandler := managev1connect.NewProgramEventSeriesServiceHandler(programevent.NewSeriesConnectHandler(programEventService), handlerOpts...)
	mux.Handle(programEventSeriesPath, programEventSeriesHandler)
	slog.Info("Registered service", "path", programEventSeriesPath)

	programEventPath, programEventHandler := managev1connect.NewProgramEventServiceHandler(programevent.NewConnectHandler(programEventService), handlerOpts...)
	mux.Handle(programEventPath, programEventHandler)
	slog.Info("Registered service", "path", programEventPath)

	tagPath, tagHandler := managev1connect.NewTagServiceHandler(referencecatalog.NewTagConnectHandler(referenceCatalog), handlerOpts...)
	mux.Handle(tagPath, tagHandler)
	slog.Info("Registered service", "path", tagPath)

	pageUnits, err := pagepostgres.NewUnits(db, pagepostgres.Ports{
		ContentBlocks: contentBlockStore, Signals: servicePublisher, Runtime: pageRuntime, MediaHydrator: fileService,
		MenuTargets: menuadapter.NewTargetLifecycle(telemetryWriter), Members: memberadapter.PageMembers{},
		ShareLinks: sharelinkadapter.PageLinks{},
	})
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize Page store: %w", err)
	}
	pageService, err := page.NewService(page.Deps{
		Units: pageUnits, SpiceDB: spicedbClient, Fence: identitystate.PostgresFence{}, Audit: telemetryWriter,
		Files: pageruntime.NewFiles(fileService),
	})
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize Page service: %w", err)
	}
	pagePath, pageHandler := managev1connect.NewPageServiceHandler(page.NewConnectHandler(pageService), handlerOpts...)
	mux.Handle(pagePath, pageHandler)
	slog.Info("Registered service", "path", pagePath)
	internalPagePath, internalPageHandler := intrav1connect.NewInternalPageServiceHandler(
		page.NewInternalConnectHandler(pageService), internalHandlerOpts...,
	)
	mux.Handle(internalPagePath, internalRPCTrust.collab(internalPageHandler))
	slog.Info("Registered internal service", "path", internalPagePath)

	menuService, err := menu.NewService(menu.Deps{
		Units: menupostgres.NewUnits(db, menupostgres.Ports{
			SiteSettings: menuadapter.NewSiteSettingsReferences(telemetryWriter),
			Checkpoints:  collaborationRuntime.Checkpoints,
		}),
		SpiceDB: spicedbClient, Fence: identitystate.PostgresFence{}, Audit: telemetryWriter,
	})
	if err != nil {
		return registeredServices{}, err
	}
	menuPath, menuHandler := managev1connect.NewMenuServiceHandler(menu.NewConnectHandler(menuService), handlerOpts...)
	mux.Handle(menuPath, menuHandler)
	slog.Info("Registered service", "path", menuPath)
	internalMenuPath, internalMenuHandler := intrav1connect.NewInternalMenuServiceHandler(
		menu.NewInternalConnectHandler(menuService), internalHandlerOpts...,
	)
	mux.Handle(internalMenuPath, internalRPCTrust.collab(internalMenuHandler))
	slog.Info("Registered internal service", "path", internalMenuPath)

	formUnits, err := formpostgres.NewUnits(db, formpostgres.Ports{
		ContentBlocks: contentBlockStore, Signals: servicePublisher, Assets: formAssets,
		OG: formogadapter.NewOG(cfg.CDNURL, ogDeps.refresher), Routes: formadapter.NewRoutes(),
		Collaboration: formadapter.NewCollaboration(spicedbClient), Members: memberadapter.PreferredLocales{},
		ShareLinks: sharelinkadapter.FormLinks{},
	})
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize Form store: %w", err)
	}
	formService, err := formdomain.NewService(formdomain.Deps{
		Units: formUnits, SpiceDB: spicedbClient, Fence: identitystate.PostgresFence{}, Audit: telemetryWriter,
		Password: passwordHasher,
	})
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize Form service: %w", err)
	}
	formPath, formHandler := managev1connect.NewFormServiceHandler(formdomain.NewConnectHandler(formService), handlerOpts...)
	mux.Handle(formPath, formHandler)
	slog.Info("Registered service", "path", formPath)
	internalFormPath, internalFormHandler := intrav1connect.NewInternalFormServiceHandler(
		formdomain.NewInternalConnectHandler(formService), internalHandlerOpts...,
	)
	mux.Handle(internalFormPath, internalRPCTrust.collab(internalFormHandler))
	slog.Info("Registered internal service", "path", internalFormPath)

	// Phase 3: Reference Data Services
	seriesRuntime := seriesadapter.NewRuntime(db, cfg.CDNURL, ogDeps.refresher)
	seriesMenuTargets := menuadapter.NewTargetLifecycle(telemetryWriter)
	seriesPostAccess := seriesadapter.PostAccess{}
	seriesUnits, err := seriespostgres.NewUnits(db, seriespostgres.Collaborators{
		Media: seriesRuntime, MenuTargets: seriesMenuTargets, Posts: postadapter.SeriesMembership{},
		PostAccess: seriesPostAccess, SpiceDB: spicedbClient, Checkpoints: collaborationRuntime.Checkpoints,
	})
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize Post Series store: %w", err)
	}
	seriesLocales, err := seriespostgres.NewLocales(db)
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize Post Series locales: %w", err)
	}
	seriesService, err := series.NewService(series.Deps{
		Units: seriesUnits, SpiceDB: spicedbClient, Fence: identitystate.PostgresFence{}, Audit: telemetryWriter,
		Media: seriesRuntime, Reads: seriesRuntime, Members: seriesadapter.NewMemberSummaries(db, cfg.CDNURL),
		Locales: seriesLocales,
	})
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize Post Series service: %w", err)
	}
	seriesPath, seriesHandler := managev1connect.NewSeriesServiceHandler(series.NewConnectHandler(seriesService), handlerOpts...)
	mux.Handle(seriesPath, seriesHandler)
	slog.Info("Registered service", "path", seriesPath)
	internalPostSeriesPath, internalPostSeriesHandler := intrav1connect.NewInternalPostSeriesServiceHandler(
		series.NewInternalConnectHandler(seriesService), internalHandlerOpts...,
	)
	mux.Handle(internalPostSeriesPath, internalRPCTrust.collab(internalPostSeriesHandler))
	slog.Info("Registered internal service", "path", internalPostSeriesPath)

	clientPath, clientHandler := managev1connect.NewClientServiceHandler(referencecatalog.NewClientConnectHandler(referenceCatalog), handlerOpts...)
	mux.Handle(clientPath, clientHandler)
	slog.Info("Registered service", "path", clientPath)

	legalUnits, err := legalpostgres.NewUnits(db, legalpostgres.Ports{
		ContentBlocks: contentBlockStore,
		Routes:        legalRuntime,
		Notices:       legaladapter.NewNoticeRuntime(),
		PreviewLinks:  sharelinkadapter.LegalPreviewLinks{},
		Members:       memberadapter.PreferredLocales{},
		Checkpoints:   collaborationRuntime.Checkpoints,
	})
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize legal store: %w", err)
	}
	legalPolicyService, err := legal.NewService(legal.Deps{
		Units:    legalUnits,
		SpiceDB:  spicedbClient,
		Fence:    identitystate.PostgresFence{},
		Audit:    telemetryWriter,
		Dispatch: legaladapter.NewNoticeDispatch(db, campaignDeliveryDispatcher),
		OGAssets: legaladapter.NewOGAssets(db, cfg.CDNURL),
		BaseURL:  cfg.SiteOrigin,
	})
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize legal policy service: %w", err)
	}
	privacyPath, privacyHandler := managev1connect.NewPrivacyServiceHandler(legal.NewPrivacyConnectHandler(legalPolicyService), handlerOpts...)
	mux.Handle(privacyPath, privacyHandler)
	slog.Info("Registered service", "path", privacyPath)

	termsPath, termsHandler := managev1connect.NewTermsServiceHandler(legal.NewTermsConnectHandler(legalPolicyService), handlerOpts...)
	mux.Handle(termsPath, termsHandler)
	slog.Info("Registered service", "path", termsPath)

	mapPlacePath, mapPlaceHandler := managev1connect.NewMapPlaceServiceHandler(referencecatalog.NewMapPlaceConnectHandler(referenceCatalog), handlerOpts...)
	mux.Handle(mapPlacePath, mapPlaceHandler)
	slog.Info("Registered service", "path", mapPlacePath)

	mapThemeService, err := maptheme.NewService(maptheme.Deps{
		Units: mapthemepostgres.NewUnits(db), SpiceDB: spicedbClient, Fence: identitystate.PostgresFence{}, Audit: telemetryWriter,
	})
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize map theme service: %w", err)
	}
	mapThemePath, mapThemeHandler := managev1connect.NewMapThemeServiceHandler(maptheme.NewConnectHandler(mapThemeService), handlerOpts...)
	mux.Handle(mapThemePath, mapThemeHandler)
	slog.Info("Registered service", "path", mapThemePath)

	emailAuthoringRuntime := emailauthoringadapter.NewRuntime()
	emailAuthoringService, err := emailauthoring.NewService(emailauthoring.Deps{
		DB: db, SpiceDB: spicedbClient, ContentBlocks: contentBlockStore, Publisher: workerPublisher,
		Runtime: emailAuthoringRuntime, RenderData: emailAuthoringRuntime, References: emailAuthoringReferences,
		Audit: telemetryWriter, CDNDomain: cfg.CDNURL, SiteOrigin: cfg.SiteOrigin,
	})
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize email authoring service: %w", err)
	}
	emailTemplatePath, emailTemplateHandler := managev1connect.NewEmailTemplateServiceHandler(emailauthoring.NewTemplateConnectHandler(emailAuthoringService), handlerOpts...)
	mux.Handle(emailTemplatePath, emailTemplateHandler)
	slog.Info("Registered service", "path", emailTemplatePath)

	emailLayoutPath, emailLayoutHandler := managev1connect.NewEmailLayoutServiceHandler(emailauthoring.NewLayoutConnectHandler(emailAuthoringService), handlerOpts...)
	mux.Handle(emailLayoutPath, emailLayoutHandler)
	slog.Info("Registered service", "path", emailLayoutPath)

	mailAdapterService, err := emaildelivery.NewService(emaildelivery.Deps{
		Units:          emaildeliverypostgres.NewUnits(db),
		SpiceDB:        spicedbClient,
		Fence:          identitystate.PostgresFence{},
		Audit:          telemetryWriter,
		Providers:      emaildeliveryadapter.MailProviders{},
		AdapterCache:   adapterLoader,
		AdapterChanges: emaildeliveryadapter.NewMailAdapterChanges(ogPublisher),
	})
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize email delivery service: %w", err)
	}
	mailAdapterPath, mailAdapterHandler := managev1connect.NewMailAdapterServiceHandler(emaildelivery.NewMailAdapterConnectHandler(mailAdapterService), handlerOpts...)
	mux.Handle(mailAdapterPath, mailAdapterHandler)
	slog.Info("Registered service", "path", mailAdapterPath)

	emailSuppressionPath, emailSuppressionHandler := managev1connect.NewEmailSuppressionServiceHandler(emaildelivery.NewEmailSuppressionConnectHandler(mailAdapterService), handlerOpts...)
	mux.Handle(emailSuppressionPath, emailSuppressionHandler)
	slog.Info("Registered service", "path", emailSuppressionPath)

	commentService, err := post.NewCommentService(post.CommentDeps{
		Units: postUnits, SpiceDB: spicedbClient, Fence: identitystate.PostgresFence{}, Members: postMembers, Audit: telemetryWriter,
	})
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize Post comment service: %w", err)
	}
	commentPath, commentHandler := managev1connect.NewCommentServiceHandler(post.NewCommentConnectHandler(commentService), handlerOpts...)
	mux.Handle(commentPath, commentHandler)
	slog.Info("Registered service", "path", commentPath)

	campaignRuntime := campaignadapter.NewRuntime(workerPublisher, telemetryWriter)
	campaignService, err := campaign.NewService(campaign.Deps{
		DB: db, SpiceDB: spicedbClient, ContentBlocks: contentBlockStore, Runtime: campaignRuntime,
		EmailAuthoring: campaignadapter.NewEmailAuthoring(), EmailRendering: campaignadapter.NewEmailRendering(),
		EmailDelivery:   campaign.NewCampaignDeliveryRuntime(spicedbClient, workerPublisher),
		AudienceTargets: campaignadapter.NewAudienceTargets(), Audit: telemetryWriter,
		CDNDomain: cfg.CDNURL, SiteOrigin: cfg.SiteOrigin,
	})
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize campaign service: %w", err)
	}
	campaignPath, campaignHandler := managev1connect.NewCampaignServiceHandler(campaign.NewConnectHandler(campaignService), handlerOpts...)
	mux.Handle(campaignPath, campaignHandler)
	slog.Info("Registered service", "path", campaignPath)

	audienceService, err := audience.NewService(audience.Deps{
		Units:            audiencepostgres.NewUnits(db, campaign.SegmentUsage{}),
		SpiceDB:          spicedbClient,
		Fence:            identitystate.PostgresFence{},
		Audit:            telemetryWriter,
		RecipientCounter: audienceadapter.NewRecipientCounter(db, spicedbClient),
	})
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize audience service: %w", err)
	}
	audiencePath, audienceHandler := managev1connect.NewAudienceServiceHandler(audience.NewConnectHandler(audienceService), handlerOpts...)
	mux.Handle(audiencePath, audienceHandler)
	slog.Info("Registered service", "path", audiencePath)

	siteSettingUnits, err := sitesettingspostgres.NewUnits(db, sitesettingspostgres.Ports{
		Assets: sitesettingsadapter.NewAssets(cfg.CDNURL), References: sitesettingsadapter.NewReferences(), OG: ogDeps.siteInvalidator(),
	})
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize site settings store: %w", err)
	}
	siteSettingService, err := sitesettings.NewService(sitesettings.Deps{
		Units: siteSettingUnits, SpiceDB: spicedbClient, Fence: identitystate.PostgresFence{}, Audit: telemetryWriter,
		SiteOrigin: cfg.SiteOrigin,
	})
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize site settings service: %w", err)
	}
	siteSettingPath, siteSettingHandler := managev1connect.NewSiteSettingServiceHandler(sitesettings.NewConnectHandler(siteSettingService), handlerOpts...)
	mux.Handle(siteSettingPath, siteSettingHandler)
	slog.Info("Registered service", "path", siteSettingPath)

	ogAdmin := og.NewAdminService(
		db,
		cfg.CDNURL,
		ogDeps.planner,
		ogDeps.resolver,
		ogDeps.collector,
		ogadapter.NewAuthorization(spicedbClient),
		labeladapter.NewGlobalReconciler(),
	)
	adminUnits, err := adminpostgres.NewUnits(db, adminpostgres.Ports{
		Members: memberadapter.DashboardMembers{}, Posts: postadapter.DashboardPosts{},
		Pages: pageadapter.DashboardPages{}, Campaigns: campaignadapter.DashboardCampaigns{},
	})
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize admin store: %w", err)
	}
	adminService, err := admin.NewService(admin.Deps{Units: adminUnits, SpiceDB: spicedbClient})
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize admin service: %w", err)
	}
	adminPath, adminHandler := managev1connect.NewAdminServiceHandler(admin.NewConnectHandler(adminService, ogAdmin), handlerOpts...)
	mux.Handle(adminPath, adminHandler)
	slog.Info("Registered service", "path", adminPath)

	shareLinkService, err := sharelink.NewService(sharelink.Deps{
		Units: sharelinkpostgres.NewUnits(db), SpiceDB: spicedbClient, Fence: identitystate.PostgresFence{}, Audit: telemetryWriter,
	})
	if err != nil {
		return registeredServices{}, err
	}
	shareLinkPath, shareLinkHandler := managev1connect.NewShareLinkServiceHandler(sharelink.NewConnectHandler(shareLinkService), handlerOpts...)
	mux.Handle(shareLinkPath, shareLinkHandler)
	slog.Info("Registered service", "path", shareLinkPath)

	internalServices, err := (internalContentServiceRegistration{
		dependencies:                deps,
		handlerOptions:              internalHandlerOpts,
		internalTrust:               internalRPCTrust,
		accountEmailChangeLifecycle: accountEmailChangeLifecycle,
		collaborationRuntime:        collaborationRuntime,
		programEvents:               programEventService,
		emailAuthoringReferences:    emailAuthoringReferences,
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
		handlerOptions:           handlerOpts,
		domainRegistry:           translationRegistry,
		emailAuthoringReferences: emailAuthoringReferences,
		legalDocuments:           legalAIDocumentService,
		seriesService:            seriesService,
		fileService:              fileService,
	}).register()
	if err != nil {
		return registeredServices{}, err
	}
	aiDocumentRegistrations := aiDocumentDomainRegistrations{}
	if aiDocumentRegistrations.post, err = aidocumentadapter.NewPostRegistration(postService); err != nil {
		return registeredServices{}, fmt.Errorf("register Post AI document domain: %w", err)
	}
	if aiDocumentRegistrations.page, err = aidocumentadapter.NewPageRegistration(pageService); err != nil {
		return registeredServices{}, fmt.Errorf("register Page AI document domain: %w", err)
	}
	if aiDocumentRegistrations.work, err = aidocumentadapter.NewWorkRegistration(workService); err != nil {
		return registeredServices{}, fmt.Errorf("register Work AI document domain: %w", err)
	}
	if aiDocumentRegistrations.programEvent, err = aidocumentadapter.NewProgramEventRegistration(programEventService); err != nil {
		return registeredServices{}, fmt.Errorf("register Program Event AI document domain: %w", err)
	}
	if aiDocumentRegistrations.release, err = aidocumentadapter.NewReleaseRegistration(releaseService); err != nil {
		return registeredServices{}, fmt.Errorf("register Release AI document domain: %w", err)
	}
	if aiDocumentRegistrations.artist, err = aidocumentadapter.NewArtistRegistration(artistService); err != nil {
		return registeredServices{}, fmt.Errorf("register Artist AI document domain: %w", err)
	}
	if aiDocumentRegistrations.label, err = aidocumentadapter.NewLabelRegistration(labelService); err != nil {
		return registeredServices{}, fmt.Errorf("register Label AI document domain: %w", err)
	}
	if aiDocumentRegistrations.menu, err = aidocumentadapter.NewMenuRegistration(menuService); err != nil {
		return registeredServices{}, fmt.Errorf("register Menu AI document domain: %w", err)
	}
	if aiDocumentRegistrations.emailTemplate, err = aidocumentadapter.NewEmailTemplateRegistration(internalEmailAuthoringService); err != nil {
		return registeredServices{}, fmt.Errorf("register Email Template AI document domain: %w", err)
	}
	if aiDocumentRegistrations.emailLayout, err = aidocumentadapter.NewEmailLayoutRegistration(internalEmailAuthoringService); err != nil {
		return registeredServices{}, fmt.Errorf("register Email Layout AI document domain: %w", err)
	}
	if aiDocumentRegistrations.campaign, err = aidocumentadapter.NewCampaignRegistration(internalCampaignService); err != nil {
		return registeredServices{}, fmt.Errorf("register Campaign AI document domain: %w", err)
	}
	if aiDocumentRegistrations.form, err = aidocumentadapter.NewFormRegistration(formService); err != nil {
		return registeredServices{}, fmt.Errorf("register Form AI document domain: %w", err)
	}
	if aiDocumentRegistrations.privacy, err = aidocumentadapter.NewPrivacyRegistration(legalAIDocumentService); err != nil {
		return registeredServices{}, fmt.Errorf("register Privacy AI document domain: %w", err)
	}
	if aiDocumentRegistrations.terms, err = aidocumentadapter.NewTermsRegistration(legalAIDocumentService); err != nil {
		return registeredServices{}, fmt.Errorf("register Terms AI document domain: %w", err)
	}
	if aiDocumentRegistrations.postSeries, err = aidocumentadapter.NewPostSeriesRegistration(
		seriesService,
	); err != nil {
		return registeredServices{}, fmt.Errorf("register Post Series AI document domain: %w", err)
	}
	aiDocumentMCP, err := newAIDocumentMCPComposition(
		aiDocumentRegistrations,
		postService,
		workService,
		pageService,
		programEventService,
		releaseService,
		artistService,
		contentMCPApplications{
			legalPolicies: legalPolicyService,
			categories:    referenceCatalog,
			tags:          referenceCatalog,
			clients:       referenceCatalog,
			mapPlaces:     referenceCatalog,
			members:       memberService,
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
		},
		translationService,
		fileService,
		aiDocumentMCPConfig{
			internalServiceSecret:     cfg.TokenSigningSecret,
			authHeaderName:            cfg.AuthHeaderName,
			internalServiceHeaderName: cfg.InternalServiceHeaderName,
			editorCollabURL:           cfg.EditorCollabURL,
			editorCollabHTTPClient:    &http.Client{Timeout: time.Duration(cfg.HTTPWriteTimeoutSec) * time.Second},
			allowedOrigins:            cfg.CORSOrigins,
		},
		deps.servicePublisher,
		sitesettingsadapter.NewMCPServerTitleSource(db),
	)
	if err != nil {
		return registeredServices{}, err
	}
	aiDocumentPath, aiDocumentHandler := managev1connect.NewAIDocumentServiceHandler(
		aiDocumentMCP.connectService,
		handlerOpts...,
	)
	mux.Handle(aiDocumentPath, aiDocumentHandler)
	slog.Info("Registered service", "path", aiDocumentPath)
	aiEditorProvider, err := llmproviders.NewGeminiProvider(llmproviders.GeminiConfig{APIKey: cfg.GoogleAIAPIKey})
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize AI Editor Gemini provider: %w", err)
	}
	aiEditorService, err := aieditor.NewService(aiDocumentMCP.editorApplication, aiEditorProvider)
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize AI Editor service: %w", err)
	}
	aiEditorPath, aiEditorHandler := managev1connect.NewAIEditorOrchestrationServiceHandler(
		aiEditorService,
		handlerOpts...,
	)
	mux.Handle(aiEditorPath, aiEditorHandler)
	slog.Info("Registered service", "path", aiEditorPath)
	if err := registerMCPRoutes(mux, aiDocumentMCP.mcpHandler); err != nil {
		return registeredServices{}, err
	}
	slog.Info("Registered MCP handler", "path", "/mcp", "methods", []string{http.MethodGet, http.MethodPost})

	internalOgService := og.NewInternalService(db, cfg.CDNURL, ogDeps.projections...)
	internalOgPath, internalOgHandler := intrav1connect.NewInternalOgServiceHandler(internalOgService, internalHandlerOpts...)
	mux.Handle(internalOgPath, internalRPCTrust.og(internalOgHandler))
	slog.Info("Registered internal service", "path", internalOgPath)
	if err := (publicServiceRegistration{
		dependencies:   deps,
		handlerOptions: publicHandlerOpts,
		fileService:    publicFileService,
		postShareLinks: postShareLinks,
		postMembers:    postMembers,
		workRuntime:    workRuntime,
		legalRuntime:   legalRuntime,
		formAssets:     formAssets,
		accounts:       accounts,
	}).register(); err != nil {
		return registeredServices{}, err
	}

	return registeredServices{
		authInterceptor:             authInterceptor,
		accountEmailChangeLifecycle: accountEmailChangeLifecycle,
		accountLifecycle:            accounts,
		mcpAuthorAdmissionHandler:   mcpAuthorAdmissionHandler,
		oathkeeperPATHandler:        oathkeeperPATHandler,
	}, nil
}
