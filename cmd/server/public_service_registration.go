package main

import (
	"fmt"
	"log/slog"

	"connectrpc.com/connect"
	accountpublic "github.com/echovisionlab/geul-api/internal/account/public"
	accountadapter "github.com/echovisionlab/geul-api/internal/adapters/account"
	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	pageruntime "github.com/echovisionlab/geul-api/internal/adapters/page/runtime"
	postadapter "github.com/echovisionlab/geul-api/internal/adapters/post"
	postruntime "github.com/echovisionlab/geul-api/internal/adapters/post/runtime"
	programeventadapter "github.com/echovisionlab/geul-api/internal/adapters/programevent"
	referencecatalogadapter "github.com/echovisionlab/geul-api/internal/adapters/referencecatalog"
	seriespublicadapter "github.com/echovisionlab/geul-api/internal/adapters/series/public"
	sharelinkadapter "github.com/echovisionlab/geul-api/internal/adapters/sharelink"
	sitemapadapter "github.com/echovisionlab/geul-api/internal/adapters/sitemap"
	sitesettingsadapter "github.com/echovisionlab/geul-api/internal/adapters/sitesettings"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	filepublic "github.com/echovisionlab/geul-api/internal/filemedia/public"
	formdomain "github.com/echovisionlab/geul-api/internal/form"
	publicform "github.com/echovisionlab/geul-api/internal/form/public"
	legalpublic "github.com/echovisionlab/geul-api/internal/legal/public"
	mapthemepublic "github.com/echovisionlab/geul-api/internal/maptheme/public"
	memberpublic "github.com/echovisionlab/geul-api/internal/member/public"
	pagepublic "github.com/echovisionlab/geul-api/internal/page/public"
	postpublic "github.com/echovisionlab/geul-api/internal/post/public"
	programeventpublic "github.com/echovisionlab/geul-api/internal/programevent/public"
	publicreferencecatalog "github.com/echovisionlab/geul-api/internal/referencecatalog/public"
	seriespublic "github.com/echovisionlab/geul-api/internal/series/public"
	sharelinkpublic "github.com/echovisionlab/geul-api/internal/sharelink/public"
	sitemappublic "github.com/echovisionlab/geul-api/internal/sitemap/public"
	publicsitesettings "github.com/echovisionlab/geul-api/internal/sitesettings/public"
	workpublic "github.com/echovisionlab/geul-api/internal/work/public"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
)

// publicServiceRegistration keeps public Connect routes together and receives
// the exact handler options configured by the composition root.
type publicServiceRegistration struct {
	dependencies     serviceRegistrationDependencies
	handlerOptions   []connect.HandlerOption
	fileService      *filepublic.FileService
	postShareLinks   postruntime.ShareLinks
	postMembers      *postadapter.MemberSummaries
	workRuntime      *workadapter.Runtime
	legalRuntime     *legaladapter.Runtime
	formDependencies formdomain.Dependencies
}

func (r publicServiceRegistration) register() error {
	deps := r.dependencies
	mux := deps.mux
	cfg := deps.cfg
	db := deps.db
	spicedbClient := deps.spicedbClient
	kratosClient := deps.kratosClient
	workerPublisher := deps.workerPublisher
	passwordHasher := deps.passwordHasher
	telemetryWriter := deps.telemetryWriter
	contentBlockStore := deps.contentBlockStore
	publicHandlerOpts := r.handlerOptions
	publicFileService := r.fileService
	postShareLinks := r.postShareLinks
	postMembers := r.postMembers
	workRuntime := r.workRuntime
	legalRuntime := r.legalRuntime
	formDependencies := r.formDependencies
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
		pagepublic.WithPageAccessPermissionChecker(spicedbClient),
	)
	publicPagePath, publicPageHandler := openv1connect.NewPageServiceHandler(publicPageService, publicHandlerOpts...)
	mux.Handle(publicPagePath, publicPageHandler)
	slog.Info("Registered public service", "path", publicPagePath)

	publicPostService, err := postpublic.NewService(postpublic.Deps{
		DB: db, CDNDomain: cfg.CDNURL, SpiceDB: spicedbClient, Files: postadapter.NewPublicFiles(db, publicFileService),
		ContentBlocks: contentBlockStore, Localizer: postadapter.NewLocalization(),
		MapPlaces: referencecatalogadapter.PublicMapPlaces{}, Members: postMembers, ShareLinks: postShareLinks,
	})
	if err != nil {
		return fmt.Errorf("initialize public Post service: %w", err)
	}
	publicPostPath, publicPostHandler := openv1connect.NewPostServiceHandler(postpublic.NewConnectHandler(publicPostService), publicHandlerOpts...)
	mux.Handle(publicPostPath, publicPostHandler)
	slog.Info("Registered public service", "path", publicPostPath)

	publicWorkService, err := workpublic.NewService(workpublic.Deps{
		DB: db, SpiceDB: spicedbClient, BlockMedia: publicFileService, Assets: workRuntime,
		Members:   workadapter.NewMemberSummaries(db, cfg.CDNURL),
		MapPlaces: referencecatalogadapter.PublicMapPlaces{}, ContentBlocks: contentBlockStore,
	})
	if err != nil {
		return fmt.Errorf("initialize public Work service: %w", err)
	}
	publicWorkPath, publicWorkHandler := openv1connect.NewWorkServiceHandler(workpublic.NewConnectHandler(publicWorkService), publicHandlerOpts...)
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

	publicSeriesService, err := seriespublic.NewService(seriespublicadapter.NewPublicReader(db, cfg.CDNURL))
	if err != nil {
		return err
	}
	publicSeriesPath, publicSeriesHandler := openv1connect.NewSeriesServiceHandler(seriespublic.NewConnectHandler(publicSeriesService), publicHandlerOpts...)
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

	publicShareLinkService, err := sharelinkpublic.NewService(sharelinkpublic.Deps{DB: db, Targets: sharelinkadapter.NewPublicTargetResolver(db)})
	if err != nil {
		return err
	}
	publicShareLinkPath, publicShareLinkHandler := openv1connect.NewShareLinkServiceHandler(sharelinkpublic.NewConnectHandler(publicShareLinkService), publicHandlerOpts...)
	mux.Handle(publicShareLinkPath, publicShareLinkHandler)
	slog.Info("Registered public service", "path", publicShareLinkPath)

	publicSitemapService := sitemappublic.NewSitemapService(
		sitemapadapter.NewPostgresStore(db),
		cfg.SiteOrigin,
	)
	publicSitemapPath, publicSitemapHandler := openv1connect.NewSitemapServiceHandler(publicSitemapService, publicHandlerOpts...)
	mux.Handle(publicSitemapPath, publicSitemapHandler)
	slog.Info("Registered public service", "path", publicSitemapPath)
	return nil
}
