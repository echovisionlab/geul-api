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
	"go.akshayshah.org/connectproto"
	"google.golang.org/protobuf/encoding/protojson"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/account"
	accountpublic "github.com/echovisionlab/geul-api/internal/account/public"
	accountadapter "github.com/echovisionlab/geul-api/internal/adapters/account"
	aidocumentadapter "github.com/echovisionlab/geul-api/internal/adapters/aidocument"
	audienceadapter "github.com/echovisionlab/geul-api/internal/adapters/audience"
	authenticationadapter "github.com/echovisionlab/geul-api/internal/adapters/authentication"
	campaignadapter "github.com/echovisionlab/geul-api/internal/adapters/campaign"
	collaborationadapter "github.com/echovisionlab/geul-api/internal/adapters/collaboration"
	emailauthoringadapter "github.com/echovisionlab/geul-api/internal/adapters/emailauthoring"
	emaildeliveryadapter "github.com/echovisionlab/geul-api/internal/adapters/emaildelivery"
	filemediaadapter "github.com/echovisionlab/geul-api/internal/adapters/filemedia"
	formadapter "github.com/echovisionlab/geul-api/internal/adapters/form"
	formogadapter "github.com/echovisionlab/geul-api/internal/adapters/form/og"
	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	mediaassetadapter "github.com/echovisionlab/geul-api/internal/adapters/mediaasset"
	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	memberpatadapter "github.com/echovisionlab/geul-api/internal/adapters/memberpat"
	menuadapter "github.com/echovisionlab/geul-api/internal/adapters/menu"
	ogadapter "github.com/echovisionlab/geul-api/internal/adapters/og"
	pageadapter "github.com/echovisionlab/geul-api/internal/adapters/page"
	pageruntime "github.com/echovisionlab/geul-api/internal/adapters/page/runtime"
	postadapter "github.com/echovisionlab/geul-api/internal/adapters/post"
	postruntime "github.com/echovisionlab/geul-api/internal/adapters/post/runtime"
	programeventadapter "github.com/echovisionlab/geul-api/internal/adapters/programevent"
	referencecatalogadapter "github.com/echovisionlab/geul-api/internal/adapters/referencecatalog"
	referencecatalogmenuadapter "github.com/echovisionlab/geul-api/internal/adapters/referencecatalog/menu"
	seriesadapter "github.com/echovisionlab/geul-api/internal/adapters/series"
	seriespublicadapter "github.com/echovisionlab/geul-api/internal/adapters/series/public"
	sharelinkadapter "github.com/echovisionlab/geul-api/internal/adapters/sharelink"
	sitemapadapter "github.com/echovisionlab/geul-api/internal/adapters/sitemap"
	sitesettingsadapter "github.com/echovisionlab/geul-api/internal/adapters/sitesettings"
	translationadapter "github.com/echovisionlab/geul-api/internal/adapters/translation"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/admin"
	"github.com/echovisionlab/geul-api/internal/ai"
	"github.com/echovisionlab/geul-api/internal/aieditor"
	"github.com/echovisionlab/geul-api/internal/audience"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authentication"
	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/collaboration"
	"github.com/echovisionlab/geul-api/internal/config"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	filepublic "github.com/echovisionlab/geul-api/internal/filemedia/public"
	formdomain "github.com/echovisionlab/geul-api/internal/form"
	publicform "github.com/echovisionlab/geul-api/internal/form/public"
	"github.com/echovisionlab/geul-api/internal/handler"
	"github.com/echovisionlab/geul-api/internal/legal"
	legalpublic "github.com/echovisionlab/geul-api/internal/legal/public"
	"github.com/echovisionlab/geul-api/internal/llm"
	"github.com/echovisionlab/geul-api/internal/maptheme"
	mapthemepublic "github.com/echovisionlab/geul-api/internal/maptheme/public"
	"github.com/echovisionlab/geul-api/internal/member"
	memberpat "github.com/echovisionlab/geul-api/internal/member/pat"
	memberpublic "github.com/echovisionlab/geul-api/internal/member/public"
	"github.com/echovisionlab/geul-api/internal/menu"
	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/page"
	pagepublic "github.com/echovisionlab/geul-api/internal/page/public"
	"github.com/echovisionlab/geul-api/internal/post"
	postpublic "github.com/echovisionlab/geul-api/internal/post/public"
	"github.com/echovisionlab/geul-api/internal/programevent"
	programeventpublic "github.com/echovisionlab/geul-api/internal/programevent/public"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	publicreferencecatalog "github.com/echovisionlab/geul-api/internal/referencecatalog/public"
	"github.com/echovisionlab/geul-api/internal/series"
	seriespublic "github.com/echovisionlab/geul-api/internal/series/public"
	"github.com/echovisionlab/geul-api/internal/sharelink"
	sharelinkpublic "github.com/echovisionlab/geul-api/internal/sharelink/public"
	sitemappublic "github.com/echovisionlab/geul-api/internal/sitemap/public"
	"github.com/echovisionlab/geul-api/internal/sitesettings"
	publicsitesettings "github.com/echovisionlab/geul-api/internal/sitesettings/public"
	"github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/transcode"
	translationcore "github.com/echovisionlab/geul-api/internal/translation"
	translationapplication "github.com/echovisionlab/geul-api/internal/translation/application"
	"github.com/echovisionlab/geul-api/internal/work"
	workpublic "github.com/echovisionlab/geul-api/internal/work/public"
	"github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
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
	metadataAIJobs          *ai.MetadataJobManager
	contentBlockStore       *contentblock.Store
	authCodeIssuanceLimiter *authentication.AuthCodeIssuanceLimiter
	adapterLoader           *email.AdapterLoader
	telemetryWriter         *telemetry.DurableWriter
	og                      *ogDependencies
}

type registeredServices struct {
	authInterceptor             *auth.AuthInterceptor
	accountEmailChangeLifecycle *account.AccountEmailChangeLifecycle
	accountLifecycle            *account.AccountLifecycleService
	mcpAuthorAdmissionHandler   http.Handler
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
	metadataAIJobs := deps.metadataAIJobs
	contentBlockStore := deps.contentBlockStore
	authCodeIssuanceLimiter := deps.authCodeIssuanceLimiter
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
	accessLogInterceptor := telemetry.NewAccessLogInterceptor(telemetryWriter)
	tracingInterceptor := telemetry.NewTracingInterceptor()
	handlerOpts := []connect.HandlerOption{
		connect.WithInterceptors(tracingInterceptor, accessLogInterceptor, authInterceptor),
		jsonCodec,
	}
	// Internal handlers do not use browser/session auth. Each registration below
	// is additionally wrapped in its exact caller-scoped service credential;
	// private network placement is only defense in depth. Public handlers still
	// use the auth interceptor so logged-in users can optionally hydrate context
	// for draft previews while anonymous public requests remain allowed.
	internalHandlerOpts := []connect.HandlerOption{
		connect.WithInterceptors(tracingInterceptor, accessLogInterceptor),
		jsonCodec,
	}
	internalRPCTrust := internalRPCTrustBoundary{
		secret:                    cfg.TokenSigningSecret,
		internalServiceHeaderName: cfg.InternalServiceHeaderName,
	}
	gatewayAuthorizationService := authentication.NewGatewayAuthorizationService(db, spicedbClient)
	gatewayAuthorizationPath, gatewayAuthorizationHandler := intrav1connect.NewInternalGatewayAuthorizationServiceHandler(
		gatewayAuthorizationService,
		internalHandlerOpts...,
	)
	mux.Handle(gatewayAuthorizationPath, internalRPCTrust.identity(gatewayAuthorizationHandler))
	publicHandlerOpts := []connect.HandlerOption{
		connect.WithInterceptors(tracingInterceptor, accessLogInterceptor, authInterceptor),
		jsonCodec,
	}

	// Register Connect services
	// FileService first (needed by other services for file deletion)
	downloadTTL := time.Duration(cfg.MediaDownloadTTLSec) * time.Second
	publicFileService := filepublic.NewFileService(
		db,
		spicedbClient,
		cfg.CDNURL,
		cfg.MediaURL,
		cfg.MediaSigningSecret,
		downloadTTL,
		filepublic.WithDownloadSegmentConfigs(mediaassetadapter.NewSegmentConfigs()),
	)
	legalRuntime := legaladapter.NewOGRuntime(cfg.CDNURL, ogDeps.planner)
	campaignDeliveryDispatcher := campaign.NewAuditedCampaignDeliveryDispatcher(
		db,
		spicedbClient,
		workerPublisher,
		telemetryWriter,
		campaign.WithLegalNoticeDeliveryPort(legalRuntime),
	)
	legalDependencies := legal.Dependencies{
		OG:     legalRuntime,
		Notice: legaladapter.NewNoticeRuntime(campaignDeliveryDispatcher),
	}
	fileService := filemedia.NewFileService(
		db,
		s3Client,
		servicePublisher,
		cfg.S3Bucket,
		cfg.CDNURL,
		cfg.MediaURL,
		cfg.MediaSigningSecret,
		transcodeTracker,
		spicedbClient,
		filemedia.WithEditorImageMaxSizeBytes(cfg.EditorImageMaxSize),
		filemedia.WithMediaDownloadTTL(downloadTTL),
		filemedia.WithFileDomainAuditWriter(telemetryWriter),
		filemedia.WithMultipartUploadPresigner(s3PresignClient),
		filemedia.WithLegalRouteIdentity(legalRuntime),
		filemedia.WithPostAccess(filemediaadapter.NewPostAccess(db, spicedbClient)),
		filemedia.WithPagePolicyAccess(pageadapter.NewPolicyAccess(page.NewPolicyAuthority(spicedbClient))),
		filemedia.WithWorkAttachment(filemediaadapter.NewWorkAttachment(db)),
		filemedia.WithWorkPolicyAccess(workadapter.NewPolicyAccess(spicedbClient)),
		filemedia.WithProgramEventAttachment(filemediaadapter.NewProgramEventAttachment(db)),
		filemedia.WithAudienceAccess(filemediaadapter.NewAudienceAccess()),
		filemedia.WithMemberSummaries(filemediaadapter.NewMemberSummaries(db, cfg.CDNURL)),
	)
	formAssets := formadapter.NewAssets(cfg.CDNURL)
	formTranslation := formadapter.NewTranslation()
	formDependencies := formdomain.Dependencies{
		ContentBlocks: contentBlockStore,
		Assets:        formAssets, OG: formogadapter.NewOG(cfg.CDNURL, ogDeps.refresher), Routes: formadapter.NewRoutes(),
		SecurityAccess: formadapter.NewSecurityAccess(telemetryWriter), Translation: formTranslation,
		PublicAssets: formAssets,
	}
	pageRuntime := pageadapter.NewRuntime(cfg.CDNURL, ogDeps.refresher)
	workRuntime := workadapter.NewRuntime(db, cfg.CDNURL, ogDeps.refresher)
	filePath, fileHandler := managev1connect.NewFileServiceHandler(fileService, handlerOpts...)
	mux.Handle(filePath, fileHandler)
	slog.Info("Registered service", "path", filePath)
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

	memberService := member.NewAuditedMemberService(
		db,
		cfg.CDNURL,
		spicedbClient,
		kratosClient,
		fileService,
		cfg.SiteOrigin,
		servicePublisher,
		telemetryWriter,
		member.WithAccountSummaryReader(memberadapter.AccountSummaryReader{}),
		member.WithAccountEmailProjection(memberadapter.AccountEmailProjection{}),
	)
	accountBaseService := account.NewAuditedAccountService(
		db,
		kratosClient,
		spicedbClient,
		cfg.SiteOrigin,
		workerPublisher,
		telemetryWriter,
		account.WithNewsletterSubscription(accountadapter.NewsletterSubscription{}),
		account.WithMemberDeletion(accountadapter.MemberDeletion{}),
		account.WithMemberEmailProjection(accountadapter.MemberEmailProjection{}),
	)
	personalAccessTokenRepository, err := memberpatadapter.NewPersonalAccessTokenRepository(db)
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize personal access token repository: %w", err)
	}
	personalAccessTokenHandlers, err := newPersonalAccessTokenComposition(
		accountBaseService,
		personalAccessTokenRepository,
		memberpatadapter.SystemClock{},
		rand.Reader,
	)
	if err != nil {
		return registeredServices{}, err
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

	accountEmailChangeLifecycle := account.NewAuditedAccountEmailChangeLifecycle(
		db,
		kratosClient,
		hooksPublisher,
		accountadapter.MemberEmailProjection{},
		telemetryWriter,
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
		authentication.NewAuthBootstrapService(db, spicedbClient, telemetryWriter, directRoles),
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
	accountLifecycle := account.NewAuditedAccountLifecycleService(
		db,
		kratosClient,
		spicedbClient,
		cfg.SiteOrigin,
		workerPublisher,
		telemetryWriter,
		account.WithLifecycleMemberDeletion(accountadapter.MemberDeletion{}),
		account.WithLifecycleMemberEmailProjection(accountadapter.MemberEmailProjection{}),
	)
	// Hooks handler (for Kratos webhooks)
	hooksHandler := handler.NewHooksHandler(
		loginHooks,
		registrationHooks,
		accountSettingsHooks,
		credentialHooks,
	)
	protectInternalHook := func(h http.HandlerFunc) http.Handler {
		return internalRPCTrust.identity(h)
	}
	mux.Handle("/hooks/after-login", protectInternalHook(hooksHandler.AfterLogin))
	mux.Handle("/hooks/reject-credential-registration", protectInternalHook(hooksHandler.RejectCredentialRegistration))
	mux.Handle("/hooks/after-settings", protectInternalHook(hooksHandler.AfterSettings))
	mux.Handle("/hooks/after-verification", protectInternalHook(hooksHandler.AfterVerification))
	mux.Handle("/hooks/pre-settings-oidc", protectInternalHook(hooksHandler.PreSettingsOIDC))
	mux.Handle("/hooks/post-settings-oidc", protectInternalHook(hooksHandler.PostSettingsOIDC))
	mux.Handle("/hooks/pre-settings-passkey", protectInternalHook(hooksHandler.PreSettingsPasskey))
	mux.Handle("/hooks/post-settings-passkey", protectInternalHook(hooksHandler.PostSettingsPasskey))
	slog.Info("Registered hooks", "paths", []string{"/hooks/after-login", "/hooks/reject-credential-registration", "/hooks/after-settings", "/hooks/after-verification", "/hooks/pre-settings-oidc", "/hooks/post-settings-oidc", "/hooks/pre-settings-passkey", "/hooks/post-settings-passkey"})
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
	mux.Handle(
		"/upload/part",
		auth.RequireGatewaySession(db, http.HandlerFunc(fileService.HandleUploadPart)),
	)
	mux.Handle(
		"/upload/prefix",
		auth.RequireGatewaySession(db, http.HandlerFunc(fileService.HandleVerifyUploadPrefix)),
	)
	mux.Handle(
		"/upload/part/presign",
		auth.RequireGatewaySession(db, http.HandlerFunc(fileService.HandlePresignUploadPart)),
	)
	mux.Handle(
		"/upload/part/confirm",
		auth.RequireGatewaySession(db, http.HandlerFunc(fileService.HandleConfirmUploadPart)),
	)
	slog.Info("Registered handlers", "paths", []string{"/upload/part", "/upload/prefix", "/upload/part/presign", "/upload/part/confirm"})

	aiService := ai.NewService(metadataAIJobs)
	aiPath, aiHandler := managev1connect.NewAIServiceHandler(aiService, handlerOpts...)
	mux.Handle(aiPath, aiHandler)
	slog.Info("Registered service", "path", aiPath)

	emailAuthoringReferences := emailauthoringadapter.NewCampaignDeliveryReferences()
	programEventRuntime := programeventadapter.NewRuntime(cfg.CDNURL)
	translationRegistry := translationadapter.NewDomainRegistry(emailAuthoringReferences, telemetryWriter)
	memberPath, memberHandler := managev1connect.NewMemberServiceHandler(memberService, handlerOpts...)
	mux.Handle(memberPath, memberHandler)
	slog.Info("Registered service", "path", memberPath)
	accountPath, accountHandler := managev1connect.NewAccountServiceHandler(accountService, handlerOpts...)
	mux.Handle(accountPath, accountHandler)
	slog.Info("Registered service", "path", accountPath)

	workService := work.NewAuditedWorkService(
		db, workRuntime, spicedbClient, kratosClient, servicePublisher, telemetryWriter,
		work.WithWorkContentBlockStore(contentBlockStore),
		work.WithWorkContentBlockMediaHydrator(fileService),
		work.WithWorkMemberSummaryLoader(workadapter.NewMemberSummaries(db, cfg.CDNURL)),
	)
	workPath, workHandler := managev1connect.NewWorkServiceHandler(workService, handlerOpts...)
	mux.Handle(workPath, workHandler)
	slog.Info("Registered service", "path", workPath)

	// Phase 2: CMS Services
	postMembers := postadapter.NewMemberSummaries(db, cfg.CDNURL)
	postShareLinks := postruntime.ShareLinks{}
	postMedia := postruntime.ContentBlockMedia{}
	postFiles := postadapter.NewFiles(fileService, publicFileService)
	postVersionRestore := postruntime.VersionRestore{}
	postService := post.NewAuditedPostService(
		db,
		cfg.CDNURL,
		ogDeps.refresher,
		spicedbClient,
		kratosClient,
		postFiles,
		servicePublisher,
		postShareLinks,
		postMedia,
		postMembers,
		postVersionRestore,
		telemetryWriter,
		post.WithPostContentBlockStore(contentBlockStore),
	)
	postPath, postHandler := managev1connect.NewPostServiceHandler(postService, handlerOpts...)
	mux.Handle(postPath, postHandler)
	slog.Info("Registered service", "path", postPath)

	referenceCatalogMenuTargets := referencecatalogmenuadapter.NewTargets(telemetryWriter)
	categoryService := referencecatalog.NewAuditedCategoryService(db, telemetryWriter, referenceCatalogMenuTargets, spicedbClient)
	categoryPath, categoryHandler := managev1connect.NewCategoryServiceHandler(categoryService, handlerOpts...)
	mux.Handle(categoryPath, categoryHandler)
	slog.Info("Registered service", "path", categoryPath)

	programEventTypeService := programevent.NewAuditedProgramEventTypeService(db, telemetryWriter, spicedbClient)
	programEventTypePath, programEventTypeHandler := managev1connect.NewProgramEventTypeServiceHandler(programEventTypeService, handlerOpts...)
	mux.Handle(programEventTypePath, programEventTypeHandler)
	slog.Info("Registered service", "path", programEventTypePath)

	programEventSeriesService := programevent.NewAuditedProgramEventSeriesService(
		db, programEventRuntime, telemetryWriter, spicedbClient,
	)
	programEventSeriesPath, programEventSeriesHandler := managev1connect.NewProgramEventSeriesServiceHandler(programEventSeriesService, handlerOpts...)
	mux.Handle(programEventSeriesPath, programEventSeriesHandler)
	slog.Info("Registered service", "path", programEventSeriesPath)

	programEventFiles := programeventadapter.NewFiles(fileService)
	programEventService := programevent.NewAuditedProgramEventService(
		db, programEventRuntime, programEventFiles, telemetryWriter, spicedbClient,
		programeventadapter.NewCreditMemberSummaries(db, cfg.CDNURL),
		programevent.WithProgramEventContentBlockStore(contentBlockStore),
		programevent.WithProgramEventAsyncPublisher(servicePublisher),
	)
	programEventPath, programEventHandler := managev1connect.NewProgramEventServiceHandler(programEventService, handlerOpts...)
	mux.Handle(programEventPath, programEventHandler)
	slog.Info("Registered service", "path", programEventPath)

	tagService := referencecatalog.NewAuditedTagService(db, telemetryWriter, referenceCatalogMenuTargets, spicedbClient)
	tagPath, tagHandler := managev1connect.NewTagServiceHandler(tagService, handlerOpts...)
	mux.Handle(tagPath, tagHandler)
	slog.Info("Registered service", "path", tagPath)

	pageFiles := pageruntime.NewFiles(fileService)
	pageService := page.NewAuditedPageService(
		db, pageRuntime, pageFiles, servicePublisher, kratosClient, telemetryWriter, spicedbClient,
		page.WithPageContentBlockStore(contentBlockStore),
		page.WithPageContentBlockMediaHydrator(fileService),
		page.WithPageMenuTargets(menu.NewTargetLifecycle(telemetryWriter)),
	)
	pagePath, pageHandler := managev1connect.NewPageServiceHandler(pageService, handlerOpts...)
	mux.Handle(pagePath, pageHandler)
	slog.Info("Registered service", "path", pagePath)

	menuService := menu.NewAuditedMenuService(
		db,
		telemetryWriter,
		menuadapter.NewSiteSettingsReferences(telemetryWriter),
		menuadapter.NewTargetReferences(),
		spicedbClient,
	)
	menuPath, menuHandler := managev1connect.NewMenuServiceHandler(menuService, handlerOpts...)
	mux.Handle(menuPath, menuHandler)
	slog.Info("Registered service", "path", menuPath)
	internalMenuService := menu.NewInternalMenuService(menuService, collaborationRuntime.Checkpoints)
	internalMenuPath, internalMenuHandler := intrav1connect.NewInternalMenuServiceHandler(
		internalMenuService, internalHandlerOpts...,
	)
	mux.Handle(internalMenuPath, internalRPCTrust.collab(internalMenuHandler))
	slog.Info("Registered internal service", "path", internalMenuPath)

	formService := formdomain.NewAuditedFormService(db, passwordHasher, kratosClient, telemetryWriter, spicedbClient, formDependencies)
	formPath, formHandler := managev1connect.NewFormServiceHandler(formService, handlerOpts...)
	mux.Handle(formPath, formHandler)
	slog.Info("Registered service", "path", formPath)

	seriesRuntime := seriesadapter.NewRuntime(db, cfg.CDNURL, ogDeps.refresher)
	seriesMenuTargets := menu.NewTargetLifecycle(telemetryWriter)
	seriesPostAccess := seriesadapter.PostAccess{}
	seriesService := series.NewAuditedSeriesService(
		db,
		seriesRuntime,
		seriesRuntime,
		seriesRuntime,
		spicedbClient,
		kratosClient,
		seriesMenuTargets,
		seriesPostAccess,
		seriesadapter.NewMemberSummaries(db, cfg.CDNURL),
		telemetryWriter,
	)
	seriesPath, seriesHandler := managev1connect.NewSeriesServiceHandler(seriesService, handlerOpts...)
	mux.Handle(seriesPath, seriesHandler)
	slog.Info("Registered service", "path", seriesPath)
	postSeriesDocuments, err := series.NewAIDocumentService(
		db, spicedbClient, seriesMenuTargets, seriesPostAccess, seriesRuntime, telemetryWriter,
	)
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize Post Series document service: %w", err)
	}
	internalPostSeriesService := series.NewInternalPostSeriesService(
		postSeriesDocuments, collaborationRuntime.Checkpoints,
	)
	internalPostSeriesPath, internalPostSeriesHandler := intrav1connect.NewInternalPostSeriesServiceHandler(
		internalPostSeriesService, internalHandlerOpts...,
	)
	mux.Handle(internalPostSeriesPath, internalRPCTrust.collab(internalPostSeriesHandler))
	slog.Info("Registered internal service", "path", internalPostSeriesPath)

	referenceCatalogAssets := referencecatalogadapter.NewAssets(cfg.CDNURL)
	referenceCatalogMembers := referencecatalogadapter.NewMemberSummaries(cfg.CDNURL)
	clientService := referencecatalog.NewAuditedClientService(db, telemetryWriter, referenceCatalogAssets, spicedbClient)
	clientPath, clientHandler := managev1connect.NewClientServiceHandler(clientService, handlerOpts...)
	mux.Handle(clientPath, clientHandler)
	slog.Info("Registered service", "path", clientPath)

	privacyService := legal.NewAuditedPrivacyService(
		db,
		cfg.SiteOrigin,
		telemetryWriter,
		spicedbClient,
		legalDependencies,
		legal.WithPrivacyContentBlockStore(contentBlockStore),
	)
	privacyPath, privacyHandler := managev1connect.NewPrivacyServiceHandler(privacyService, handlerOpts...)
	mux.Handle(privacyPath, privacyHandler)
	slog.Info("Registered service", "path", privacyPath)

	termsService := legal.NewAuditedTermsService(
		db,
		cfg.SiteOrigin,
		telemetryWriter,
		spicedbClient,
		legalDependencies,
		legal.WithTermsContentBlockStore(contentBlockStore),
	)
	termsPath, termsHandler := managev1connect.NewTermsServiceHandler(termsService, handlerOpts...)
	mux.Handle(termsPath, termsHandler)
	slog.Info("Registered service", "path", termsPath)

	mapPlaceService := referencecatalog.NewAuditedMapPlaceService(
		db, telemetryWriter, referenceCatalogAssets, referenceCatalogMembers, spicedbClient,
	)
	mapPlacePath, mapPlaceHandler := managev1connect.NewMapPlaceServiceHandler(mapPlaceService, handlerOpts...)
	mux.Handle(mapPlacePath, mapPlaceHandler)
	slog.Info("Registered service", "path", mapPlacePath)

	mapThemeService := maptheme.NewAuditedMapThemeService(db, telemetryWriter, spicedbClient)
	mapThemePath, mapThemeHandler := managev1connect.NewMapThemeServiceHandler(mapThemeService, handlerOpts...)
	mux.Handle(mapThemePath, mapThemeHandler)
	slog.Info("Registered service", "path", mapThemePath)

	emailAuthoringRuntime := emailauthoringadapter.NewRuntime()
	emailTemplateService := emailauthoring.NewAuditedEmailTemplateService(
		db, workerPublisher, emailAuthoringRuntime, cfg.CDNURL, cfg.SiteOrigin, telemetryWriter, spicedbClient,
		emailauthoring.WithEmailTemplateContentBlockStore(contentBlockStore),
		emailauthoring.WithEmailTemplateRenderDataBuilder(emailAuthoringRuntime),
		emailauthoring.WithEmailTemplateCampaignDeliveryReferences(emailAuthoringReferences),
	)
	emailTemplatePath, emailTemplateHandler := managev1connect.NewEmailTemplateServiceHandler(emailTemplateService, handlerOpts...)
	mux.Handle(emailTemplatePath, emailTemplateHandler)
	slog.Info("Registered service", "path", emailTemplatePath)

	emailLayoutService := emailauthoring.NewAuditedEmailLayoutService(
		db,
		cfg.CDNURL,
		cfg.SiteOrigin,
		telemetryWriter,
		spicedbClient,
		emailauthoring.WithEmailLayoutRenderDataBuilder(emailAuthoringRuntime),
		emailauthoring.WithEmailLayoutCampaignDeliveryReferences(emailAuthoringReferences),
		emailauthoring.WithEmailLayoutContentBlockStore(contentBlockStore),
	)
	emailLayoutPath, emailLayoutHandler := managev1connect.NewEmailLayoutServiceHandler(emailLayoutService, handlerOpts...)
	mux.Handle(emailLayoutPath, emailLayoutHandler)
	slog.Info("Registered service", "path", emailLayoutPath)

	mailAdapterService := emaildelivery.NewAuditedMailAdapterService(db, adapterLoader, ogPublisher, telemetryWriter, spicedbClient)
	mailAdapterPath, mailAdapterHandler := managev1connect.NewMailAdapterServiceHandler(mailAdapterService, handlerOpts...)
	mux.Handle(mailAdapterPath, mailAdapterHandler)
	slog.Info("Registered service", "path", mailAdapterPath)

	emailSuppressionService := emaildelivery.NewAuditedEmailSuppressionService(db, telemetryWriter, spicedbClient)
	emailSuppressionPath, emailSuppressionHandler := managev1connect.NewEmailSuppressionServiceHandler(emailSuppressionService, handlerOpts...)
	mux.Handle(emailSuppressionPath, emailSuppressionHandler)
	slog.Info("Registered service", "path", emailSuppressionPath)

	commentService := post.NewCommentService(db, spicedbClient, postMembers)
	commentPath, commentHandler := managev1connect.NewCommentServiceHandler(commentService, handlerOpts...)
	mux.Handle(commentPath, commentHandler)
	slog.Info("Registered service", "path", commentPath)

	campaignRuntime := campaignadapter.NewRuntime(workerPublisher, telemetryWriter)
	campaignService := campaign.NewAuditedCampaignService(
		db, campaignRuntime, cfg.CDNURL, cfg.SiteOrigin, telemetryWriter, spicedbClient,
		campaign.WithCampaignContentBlockStore(contentBlockStore),
		campaign.WithCampaignEmailAuthoring(campaignadapter.NewEmailAuthoring()),
		campaign.WithCampaignEmailRendering(campaignadapter.NewEmailRendering()),
		campaign.WithCampaignEmailDelivery(campaign.NewCampaignDeliveryRuntime(spicedbClient, workerPublisher)),
		campaign.WithCampaignAudienceTargets(campaignadapter.NewAudienceTargets()),
	)
	campaignPath, campaignHandler := managev1connect.NewCampaignServiceHandler(campaignService, handlerOpts...)
	mux.Handle(campaignPath, campaignHandler)
	slog.Info("Registered service", "path", campaignPath)

	audienceService := audience.NewAuditedAudienceService(
		db,
		telemetryWriter,
		spicedbClient,
		audienceadapter.NewRecipientCounter(db, spicedbClient),
		audienceadapter.MemberReferences{},
	)
	audiencePath, audienceHandler := managev1connect.NewAudienceServiceHandler(audienceService, handlerOpts...)
	mux.Handle(audiencePath, audienceHandler)
	slog.Info("Registered service", "path", audiencePath)

	siteSettingService := sitesettings.NewAuditedSiteSettingService(
		db,
		cfg.SiteOrigin,
		sitesettingsadapter.NewAssets(cfg.CDNURL),
		sitesettingsadapter.NewReferences(),
		ogDeps.siteInvalidator(),
		telemetryWriter,
		spicedbClient,
	)
	siteSettingPath, siteSettingHandler := managev1connect.NewSiteSettingServiceHandler(siteSettingService, handlerOpts...)
	mux.Handle(siteSettingPath, siteSettingHandler)
	slog.Info("Registered service", "path", siteSettingPath)

	ogAdmin := og.NewAdminService(
		db,
		cfg.CDNURL,
		ogDeps.planner,
		ogDeps.resolver,
		ogDeps.collector,
		ogadapter.NewAuthorization(spicedbClient),
		og.NoopGlobalReconciler{},
	)
	adminService := admin.NewService(db, spicedbClient, ogAdmin)
	adminPath, adminHandler := managev1connect.NewAdminServiceHandler(adminService, handlerOpts...)
	mux.Handle(adminPath, adminHandler)
	slog.Info("Registered service", "path", adminPath)

	shareLinkService := sharelink.NewService(
		db,
		sharelinkadapter.NewAuthority(db, spicedbClient, telemetryWriter),
	)
	shareLinkPath, shareLinkHandler := managev1connect.NewShareLinkServiceHandler(shareLinkService, handlerOpts...)
	mux.Handle(shareLinkPath, shareLinkHandler)
	slog.Info("Registered service", "path", shareLinkPath)

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
		return registeredServices{}, fmt.Errorf("initialize Legal AI document service: %w", err)
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
			Port:   translationadapter.NewPostInterchange(post.NewTranslationInterchange(telemetryWriter)),
		},
		{
			Domain: translationcore.KindWork,
			Port: translationadapter.NewWorkInterchangePort(
				telemetryWriter, sharedtelemetry.NewWorkLocaleContentAuditRecord,
			),
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
		return registeredServices{}, fmt.Errorf("initialize Translation interchange registry: %w", err)
	}
	translationXLIFFFiles, err := filemediaadapter.NewTranslationXLIFFFiles(fileService)
	if err != nil {
		return registeredServices{}, fmt.Errorf("initialize Translation XLIFF File runtime: %w", err)
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

	aiDocumentRegistrations := aiDocumentDomainRegistrations{}
	if aiDocumentRegistrations.post, err = aidocumentadapter.NewPostRegistration(postService); err != nil {
		return registeredServices{}, fmt.Errorf("register Post AI document domain: %w", err)
	}
	if aiDocumentRegistrations.page, err = aidocumentadapter.NewPageRegistration(internalPageService); err != nil {
		return registeredServices{}, fmt.Errorf("register Page AI document domain: %w", err)
	}
	if aiDocumentRegistrations.work, err = aidocumentadapter.NewWorkRegistration(internalWorkService); err != nil {
		return registeredServices{}, fmt.Errorf("register Work AI document domain: %w", err)
	}
	if aiDocumentRegistrations.programEvent, err = aidocumentadapter.NewProgramEventRegistration(programEventService); err != nil {
		return registeredServices{}, fmt.Errorf("register Program Event AI document domain: %w", err)
	}
	if aiDocumentRegistrations.menu, err = aidocumentadapter.NewMenuRegistration(menuService); err != nil {
		return registeredServices{}, fmt.Errorf("register Menu AI document domain: %w", err)
	}
	if aiDocumentRegistrations.emailTemplate, err = aidocumentadapter.NewEmailTemplateRegistration(internalEmailTemplateService); err != nil {
		return registeredServices{}, fmt.Errorf("register Email Template AI document domain: %w", err)
	}
	if aiDocumentRegistrations.emailLayout, err = aidocumentadapter.NewEmailLayoutRegistration(emailLayoutService); err != nil {
		return registeredServices{}, fmt.Errorf("register Email Layout AI document domain: %w", err)
	}
	if aiDocumentRegistrations.campaign, err = aidocumentadapter.NewCampaignRegistration(internalCampaignService); err != nil {
		return registeredServices{}, fmt.Errorf("register Campaign AI document domain: %w", err)
	}
	if aiDocumentRegistrations.form, err = aidocumentadapter.NewFormRegistration(internalFormService); err != nil {
		return registeredServices{}, fmt.Errorf("register Form AI document domain: %w", err)
	}
	if aiDocumentRegistrations.privacy, err = aidocumentadapter.NewPrivacyRegistration(legalAIDocumentService); err != nil {
		return registeredServices{}, fmt.Errorf("register Privacy AI document domain: %w", err)
	}
	if aiDocumentRegistrations.terms, err = aidocumentadapter.NewTermsRegistration(legalAIDocumentService); err != nil {
		return registeredServices{}, fmt.Errorf("register Terms AI document domain: %w", err)
	}
	if aiDocumentRegistrations.postSeries, err = aidocumentadapter.NewPostSeriesRegistration(
		postSeriesDocuments,
	); err != nil {
		return registeredServices{}, fmt.Errorf("register Post Series AI document domain: %w", err)
	}
	aiDocumentMCP, err := newAIDocumentMCPComposition(
		aiDocumentRegistrations,
		postService,
		workService,
		pageService,
		programEventService,
		contentReferenceApplications{
			categories: categoryService,
			tags:       tagService,
			clients:    clientService,
			mapPlaces:  mapPlaceService,
			members:    memberService,
			files:      fileService,
		},
		translationService,
		fileService,
		cfg.TokenSigningSecret,
		cfg.AuthHeaderName,
		cfg.InternalServiceHeaderName,
		cfg.EditorCollabURL,
		&http.Client{Timeout: time.Duration(cfg.HTTPWriteTimeoutSec) * time.Second},
		deps.servicePublisher,
		cfg.CORSOrigins,
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
	aiEditorProvider, err := llm.NewGeminiProvider(llm.GeminiConfig{APIKey: cfg.GoogleAIAPIKey})
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
	// Public API services (no auth required - exposed via Oathkeeper public rules)
	manifestService := publicsitesettings.NewManifestService(
		cfg.SiteOrigin,
		sitesettingsadapter.NewPublicProjection(
			db,
			sitesettingsadapter.NewAssets(cfg.CDNURL),
			sitesettingsadapter.ManifestMenus{},
		),
		spicedbClient,
	)
	manifestPath, manifestHandler := openv1connect.NewManifestServiceHandler(manifestService, publicHandlerOpts...)
	mux.Handle(manifestPath, manifestHandler)
	slog.Info("Registered public service", "path", manifestPath)

	publicFilePath, publicFileHandler := openv1connect.NewFileServiceHandler(publicFileService, publicHandlerOpts...)
	mux.Handle(publicFilePath, publicFileHandler)
	slog.Info("Registered public service", "path", publicFilePath)

	pagePublicAccess := pageruntime.NewPublicAccess(spicedbClient, postShareLinks)
	publicPageService := pagepublic.NewPageService(
		db,
		pagePublicAccess,
		pagePublicAccess,
		pageruntime.NewPublicMedia(publicFileService),
		pagepublic.WithPageContentBlockStore(contentBlockStore),
	)
	publicPagePath, publicPageHandler := openv1connect.NewPageServiceHandler(publicPageService, publicHandlerOpts...)
	mux.Handle(publicPagePath, publicPageHandler)
	slog.Info("Registered public service", "path", publicPagePath)

	publicPostService := postpublic.NewPostService(
		db,
		cfg.CDNURL,
		spicedbClient,
		postadapter.NewPublicFiles(db, publicFileService),
		postadapter.NewLocalization(),
		referencecatalogadapter.PublicMapPlaces{},
		postMembers,
		postShareLinks,
		postpublic.WithPostContentBlockStore(contentBlockStore),
	)
	publicPostPath, publicPostHandler := openv1connect.NewPostServiceHandler(publicPostService, publicHandlerOpts...)
	mux.Handle(publicPostPath, publicPostHandler)
	slog.Info("Registered public service", "path", publicPostPath)

	publicWorkService := workpublic.NewWorkService(
		db,
		spicedbClient,
		publicFileService,
		workRuntime,
		workadapter.NewMemberSummaries(db, cfg.CDNURL),
		referencecatalogadapter.PublicMapPlaces{},
		workpublic.WithWorkContentBlockStore(contentBlockStore),
	)
	publicWorkPath, publicWorkHandler := openv1connect.NewWorkServiceHandler(publicWorkService, publicHandlerOpts...)
	mux.Handle(publicWorkPath, publicWorkHandler)
	slog.Info("Registered public service", "path", publicWorkPath)

	publicReferenceCatalogAssets := referencecatalogadapter.NewPublicAssets(cfg.CDNURL)
	publicClientService := publicreferencecatalog.NewClientService(db, publicReferenceCatalogAssets)
	publicClientPath, publicClientHandler := openv1connect.NewClientServiceHandler(publicClientService, publicHandlerOpts...)
	mux.Handle(publicClientPath, publicClientHandler)
	slog.Info("Registered public service", "path", publicClientPath)

	publicFormService := publicform.NewAuditedFormService(db, passwordHasher, spicedbClient, telemetryWriter, formDependencies)
	publicFormPath, publicFormHandler := openv1connect.NewFormServiceHandler(publicFormService, publicHandlerOpts...)
	mux.Handle(publicFormPath, publicFormHandler)
	slog.Info("Registered public service", "path", publicFormPath)

	publicMemberService := memberpublic.NewMemberService(db, cfg.CDNURL, spicedbClient)
	publicMemberPath, publicMemberHandler := openv1connect.NewMemberServiceHandler(publicMemberService, publicHandlerOpts...)
	mux.Handle(publicMemberPath, publicMemberHandler)
	slog.Info("Registered public service", "path", publicMemberPath)
	publicAccountService := accountpublic.NewAuditedAccountService(
		db,
		kratosClient,
		spicedbClient,
		cfg.SiteOrigin,
		workerPublisher,
		accountadapter.MemberDeletion{},
		accountadapter.MemberEmailProjection{},
		telemetryWriter,
	)
	publicAccountPath, publicAccountHandler := openv1connect.NewAccountServiceHandler(publicAccountService, publicHandlerOpts...)
	mux.Handle(publicAccountPath, publicAccountHandler)
	slog.Info("Registered public service", "path", publicAccountPath)

	publicPrivacyService := legalpublic.NewPrivacyServiceWithContentBlocks(db, contentBlockStore, legalRuntime)
	publicPrivacyPath, publicPrivacyHandler := openv1connect.NewPrivacyServiceHandler(publicPrivacyService, publicHandlerOpts...)
	mux.Handle(publicPrivacyPath, publicPrivacyHandler)
	slog.Info("Registered public service", "path", publicPrivacyPath)

	publicTermsService := legalpublic.NewTermsServiceWithContentBlocks(db, contentBlockStore, legalRuntime)
	publicTermsPath, publicTermsHandler := openv1connect.NewTermsServiceHandler(publicTermsService, publicHandlerOpts...)
	mux.Handle(publicTermsPath, publicTermsHandler)
	slog.Info("Registered public service", "path", publicTermsPath)

	publicCategoryService := publicreferencecatalog.NewCategoryService(db)
	publicCategoryPath, publicCategoryHandler := openv1connect.NewCategoryServiceHandler(publicCategoryService, publicHandlerOpts...)
	mux.Handle(publicCategoryPath, publicCategoryHandler)
	slog.Info("Registered public service", "path", publicCategoryPath)

	publicSeriesService := seriespublic.NewSeriesService(seriespublicadapter.NewPublicReader(db, cfg.CDNURL))
	publicSeriesPath, publicSeriesHandler := openv1connect.NewSeriesServiceHandler(publicSeriesService, publicHandlerOpts...)
	mux.Handle(publicSeriesPath, publicSeriesHandler)
	slog.Info("Registered public service", "path", publicSeriesPath)

	publicTagService := publicreferencecatalog.NewTagService(db)
	publicTagPath, publicTagHandler := openv1connect.NewTagServiceHandler(publicTagService, publicHandlerOpts...)
	mux.Handle(publicTagPath, publicTagHandler)
	slog.Info("Registered public service", "path", publicTagPath)

	publicNewsletterService := memberpublic.NewAuditedNewsletterService(db, cfg.TokenSigningSecret, telemetryWriter)
	publicNewsletterPath, publicNewsletterHandler := openv1connect.NewNewsletterServiceHandler(publicNewsletterService, publicHandlerOpts...)
	mux.Handle(publicNewsletterPath, publicNewsletterHandler)
	slog.Info("Registered public service", "path", publicNewsletterPath)

	publicMapPlaceService := publicreferencecatalog.NewMapPlaceService(db, publicReferenceCatalogAssets)
	publicMapPlacePath, publicMapPlaceHandler := openv1connect.NewMapPlaceServiceHandler(publicMapPlaceService, publicHandlerOpts...)
	mux.Handle(publicMapPlacePath, publicMapPlaceHandler)
	slog.Info("Registered public service", "path", publicMapPlacePath)

	publicMapThemeService := mapthemepublic.NewMapThemeService(db)
	publicMapThemePath, publicMapThemeHandler := openv1connect.NewMapThemeServiceHandler(publicMapThemeService, publicHandlerOpts...)
	mux.Handle(publicMapThemePath, publicMapThemeHandler)
	slog.Info("Registered public service", "path", publicMapThemePath)

	publicProgramEventTypeService := programeventpublic.NewProgramEventTypeService(db)
	publicProgramEventTypePath, publicProgramEventTypeHandler := openv1connect.NewProgramEventTypeServiceHandler(publicProgramEventTypeService, publicHandlerOpts...)
	mux.Handle(publicProgramEventTypePath, publicProgramEventTypeHandler)
	slog.Info("Registered public service", "path", publicProgramEventTypePath)

	publicProgramEventAssets := programeventadapter.NewPublicAssets(db, cfg.CDNURL)
	publicProgramEventFiles := programeventadapter.NewPublicFiles(publicFileService)
	publicProgramEventSeriesService := programeventpublic.NewProgramEventSeriesService(db, publicProgramEventAssets)
	publicProgramEventSeriesPath, publicProgramEventSeriesHandler := openv1connect.NewProgramEventSeriesServiceHandler(publicProgramEventSeriesService, publicHandlerOpts...)
	mux.Handle(publicProgramEventSeriesPath, publicProgramEventSeriesHandler)
	slog.Info("Registered public service", "path", publicProgramEventSeriesPath)

	publicProgramEventService := programeventpublic.NewProgramEventService(
		db,
		publicProgramEventAssets,
		programeventadapter.NewPublicCreditMemberSummaries(db, cfg.CDNURL),
		programeventpublic.WithProgramEventContentBlockStore(contentBlockStore),
		programeventpublic.WithProgramEventFileService(publicProgramEventFiles),
	)
	publicProgramEventPath, publicProgramEventHandler := openv1connect.NewProgramEventServiceHandler(publicProgramEventService, publicHandlerOpts...)
	mux.Handle(publicProgramEventPath, publicProgramEventHandler)
	slog.Info("Registered public service", "path", publicProgramEventPath)

	publicShareLinkService := sharelinkpublic.NewService(db, sharelinkadapter.NewPublicTargetResolver(db))
	publicShareLinkPath, publicShareLinkHandler := openv1connect.NewShareLinkServiceHandler(publicShareLinkService, publicHandlerOpts...)
	mux.Handle(publicShareLinkPath, publicShareLinkHandler)
	slog.Info("Registered public service", "path", publicShareLinkPath)

	publicSitemapService := sitemappublic.NewSitemapService(
		sitemapadapter.NewPostgresStore(db),
		cfg.SiteOrigin,
	)
	publicSitemapPath, publicSitemapHandler := openv1connect.NewSitemapServiceHandler(publicSitemapService, publicHandlerOpts...)
	mux.Handle(publicSitemapPath, publicSitemapHandler)
	slog.Info("Registered public service", "path", publicSitemapPath)

	return registeredServices{
		authInterceptor:             authInterceptor,
		accountEmailChangeLifecycle: accountEmailChangeLifecycle,
		accountLifecycle:            accountLifecycle,
		mcpAuthorAdmissionHandler:   mcpAuthorAdmissionHandler,
	}, nil
}
