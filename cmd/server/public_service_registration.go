package main

import (
	"fmt"
	"log/slog"

	"connectrpc.com/connect"
	accountdomain "github.com/echovisionlab/geul-api/internal/account"
	accountpublic "github.com/echovisionlab/geul-api/internal/account/public"
	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
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
	mapthemepostgres "github.com/echovisionlab/geul-api/internal/maptheme/postgres"
	mapthemepublic "github.com/echovisionlab/geul-api/internal/maptheme/public"
	memberpublic "github.com/echovisionlab/geul-api/internal/member/public"
	pagepublic "github.com/echovisionlab/geul-api/internal/page/public"
	postpublic "github.com/echovisionlab/geul-api/internal/post/public"
	programeventpublic "github.com/echovisionlab/geul-api/internal/programevent/public"
	publicreferencecatalog "github.com/echovisionlab/geul-api/internal/referencecatalog/public"
	seriespublic "github.com/echovisionlab/geul-api/internal/series/public"
	sharelinkpostgres "github.com/echovisionlab/geul-api/internal/sharelink/postgres"
	sharelinkpublic "github.com/echovisionlab/geul-api/internal/sharelink/public"
	sitemappublic "github.com/echovisionlab/geul-api/internal/sitemap/public"
	publicsitesettings "github.com/echovisionlab/geul-api/internal/sitesettings/public"
	workpublic "github.com/echovisionlab/geul-api/internal/work/public"
	workpublicpostgres "github.com/echovisionlab/geul-api/internal/work/public/postgres"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
)

// publicServiceRegistration keeps public Connect routes together and receives
// the exact handler options configured by the composition root.
type publicServiceRegistration struct {
	dependencies   serviceRegistrationDependencies
	handlerOptions []connect.HandlerOption
	fileService    *filepublic.Service
	postShareLinks postruntime.ShareLinks
	postMembers    *postadapter.MemberSummaries
	workRuntime    *workadapter.Runtime
	legalRuntime   *legaladapter.Runtime
	formAssets     formdomain.PublicAssets
	accounts       *accountdomain.Service
}

func (r publicServiceRegistration) register() error {
	deps := r.dependencies
	mux := deps.mux
	cfg := deps.cfg
	db := deps.db
	spicedbClient := deps.spicedbClient
	passwordHasher := deps.passwordHasher
	telemetryWriter := deps.telemetryWriter
	contentBlockStore := deps.contentBlockStore
	publicHandlerOpts := r.handlerOptions
	publicFileService := r.fileService
	postShareLinks := r.postShareLinks
	postMembers := r.postMembers
	workRuntime := r.workRuntime
	legalRuntime := r.legalRuntime
	// Public API services (no auth required - exposed via Oathkeeper public rules)
	manifestService, err := publicsitesettings.NewService(publicsitesettings.Deps{
		Projection: sitesettingsadapter.NewPublicProjection(
			db,
			sitesettingsadapter.NewAssets(cfg.CDNURL),
			sitesettingsadapter.ManifestMenus{},
		),
		SpiceDB:    spicedbClient,
		SiteOrigin: cfg.SiteOrigin,
	})
	if err != nil {
		return fmt.Errorf("initialize public site settings service: %w", err)
	}
	manifestPath, manifestHandler := openv1connect.NewManifestServiceHandler(publicsitesettings.NewConnectHandler(manifestService), publicHandlerOpts...)
	mux.Handle(manifestPath, manifestHandler)
	slog.Info("Registered public service", "path", manifestPath)

	publicFilePath, publicFileHandler := openv1connect.NewFileServiceHandler(filepublic.NewConnectHandler(publicFileService), publicHandlerOpts...)
	mux.Handle(publicFilePath, publicFileHandler)
	slog.Info("Registered public service", "path", publicFilePath)

	pagePublicAccess := pageruntime.NewPublicAccess(spicedbClient, postShareLinks)
	publicPageService, err := pagepublic.NewService(pagepublic.Deps{
		DB: db, DraftAccess: pagePublicAccess, ShareLinks: pagePublicAccess,
		Media: pageruntime.NewPublicMedia(publicFileService), Blocks: contentBlockStore, Permissions: spicedbClient,
	})
	if err != nil {
		return err
	}
	publicPagePath, publicPageHandler := openv1connect.NewPageServiceHandler(pagepublic.NewConnectHandler(publicPageService), publicHandlerOpts...)
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

	publicWorkUnits, err := workpublicpostgres.NewUnits(db, workpublicpostgres.Ports{
		ContentBlocks: contentBlockStore, MapPlaces: referencecatalogadapter.PublicMapPlaces{}, Assets: workRuntime,
	})
	if err != nil {
		return fmt.Errorf("initialize public Work store: %w", err)
	}
	publicWorkService, err := workpublic.NewService(workpublic.Deps{
		Units: publicWorkUnits, SpiceDB: spicedbClient, BlockMedia: publicFileService, Assets: workRuntime,
		Members: workadapter.NewMemberSummaries(db, cfg.CDNURL),
	})
	if err != nil {
		return fmt.Errorf("initialize public Work service: %w", err)
	}
	publicWorkPath, publicWorkHandler := openv1connect.NewWorkServiceHandler(workpublic.NewConnectHandler(publicWorkService), publicHandlerOpts...)
	mux.Handle(publicWorkPath, publicWorkHandler)
	slog.Info("Registered public service", "path", publicWorkPath)

	publicReferenceCatalog, err := publicreferencecatalog.NewService(publicreferencecatalog.Deps{
		DB: db, Assets: referencecatalogadapter.NewPublicAssets(cfg.CDNURL),
	})
	if err != nil {
		return fmt.Errorf("initialize public reference catalog service: %w", err)
	}
	publicClientPath, publicClientHandler := openv1connect.NewClientServiceHandler(publicreferencecatalog.NewClientConnectHandler(publicReferenceCatalog), publicHandlerOpts...)
	mux.Handle(publicClientPath, publicClientHandler)
	slog.Info("Registered public service", "path", publicClientPath)

	publicFormService, err := publicform.NewService(publicform.Deps{
		DB: db, SpiceDB: spicedbClient, Password: passwordHasher, Assets: r.formAssets,
		Members: memberadapter.PreferredLocales{}, ShareLinks: sharelinkadapter.FormLinks{}, Audit: telemetryWriter,
	})
	if err != nil {
		return fmt.Errorf("initialize public Form service: %w", err)
	}
	publicFormPath, publicFormHandler := openv1connect.NewFormServiceHandler(publicform.NewConnectHandler(publicFormService), publicHandlerOpts...)
	mux.Handle(publicFormPath, publicFormHandler)
	slog.Info("Registered public service", "path", publicFormPath)

	publicMembers, err := memberpublic.NewService(memberpublic.Deps{
		DB: db, SpiceDB: spicedbClient, Audit: telemetryWriter, TokenSecret: cfg.TokenSigningSecret, CDNDomain: cfg.CDNURL,
	})
	if err != nil {
		return fmt.Errorf("initialize public member service: %w", err)
	}
	publicMemberPath, publicMemberHandler := openv1connect.NewMemberServiceHandler(memberpublic.NewMemberConnectHandler(publicMembers), publicHandlerOpts...)
	mux.Handle(publicMemberPath, publicMemberHandler)
	slog.Info("Registered public service", "path", publicMemberPath)
	publicAccountPath, publicAccountHandler := openv1connect.NewAccountServiceHandler(accountpublic.NewConnectHandler(r.accounts), publicHandlerOpts...)
	mux.Handle(publicAccountPath, publicAccountHandler)
	slog.Info("Registered public service", "path", publicAccountPath)

	publicLegalService, err := legalpublic.NewService(legalpublic.Deps{DB: db, ContentBlocks: contentBlockStore, Media: legalRuntime})
	if err != nil {
		return err
	}
	publicPrivacyPath, publicPrivacyHandler := openv1connect.NewPrivacyServiceHandler(legalpublic.NewPrivacyConnectHandler(publicLegalService), publicHandlerOpts...)
	mux.Handle(publicPrivacyPath, publicPrivacyHandler)
	slog.Info("Registered public service", "path", publicPrivacyPath)

	publicTermsPath, publicTermsHandler := openv1connect.NewTermsServiceHandler(legalpublic.NewTermsConnectHandler(publicLegalService), publicHandlerOpts...)
	mux.Handle(publicTermsPath, publicTermsHandler)
	slog.Info("Registered public service", "path", publicTermsPath)

	publicCategoryPath, publicCategoryHandler := openv1connect.NewCategoryServiceHandler(publicreferencecatalog.NewCategoryConnectHandler(publicReferenceCatalog), publicHandlerOpts...)
	mux.Handle(publicCategoryPath, publicCategoryHandler)
	slog.Info("Registered public service", "path", publicCategoryPath)

	publicSeriesService, err := seriespublic.NewService(seriespublicadapter.NewPublicReader(db, cfg.CDNURL))
	if err != nil {
		return err
	}
	publicSeriesPath, publicSeriesHandler := openv1connect.NewSeriesServiceHandler(seriespublic.NewConnectHandler(publicSeriesService), publicHandlerOpts...)
	mux.Handle(publicSeriesPath, publicSeriesHandler)
	slog.Info("Registered public service", "path", publicSeriesPath)

	publicTagPath, publicTagHandler := openv1connect.NewTagServiceHandler(publicreferencecatalog.NewTagConnectHandler(publicReferenceCatalog), publicHandlerOpts...)
	mux.Handle(publicTagPath, publicTagHandler)
	slog.Info("Registered public service", "path", publicTagPath)

	publicNewsletterPath, publicNewsletterHandler := openv1connect.NewNewsletterServiceHandler(memberpublic.NewNewsletterConnectHandler(publicMembers), publicHandlerOpts...)
	mux.Handle(publicNewsletterPath, publicNewsletterHandler)
	slog.Info("Registered public service", "path", publicNewsletterPath)

	publicMapPlacePath, publicMapPlaceHandler := openv1connect.NewMapPlaceServiceHandler(publicreferencecatalog.NewMapPlaceConnectHandler(publicReferenceCatalog), publicHandlerOpts...)
	mux.Handle(publicMapPlacePath, publicMapPlaceHandler)
	slog.Info("Registered public service", "path", publicMapPlacePath)

	publicMapThemeService, err := mapthemepublic.NewService(mapthemepublic.Deps{Units: mapthemepostgres.NewUnits(db)})
	if err != nil {
		return fmt.Errorf("initialize public map theme service: %w", err)
	}
	publicMapThemePath, publicMapThemeHandler := openv1connect.NewMapThemeServiceHandler(mapthemepublic.NewConnectHandler(publicMapThemeService), publicHandlerOpts...)
	mux.Handle(publicMapThemePath, publicMapThemeHandler)
	slog.Info("Registered public service", "path", publicMapThemePath)

	publicProgramEvents, err := programeventpublic.NewService(programeventpublic.Deps{
		DB: db, Assets: programeventadapter.NewPublicAssets(db, cfg.CDNURL), ContentBlocks: contentBlockStore,
		Files:         programeventadapter.NewPublicFiles(publicFileService),
		CreditMembers: programeventadapter.NewPublicCreditMemberSummaries(db, cfg.CDNURL),
	})
	if err != nil {
		return fmt.Errorf("initialize public program event service: %w", err)
	}
	publicProgramEventTypePath, publicProgramEventTypeHandler := openv1connect.NewProgramEventTypeServiceHandler(programeventpublic.NewTypeConnectHandler(publicProgramEvents), publicHandlerOpts...)
	mux.Handle(publicProgramEventTypePath, publicProgramEventTypeHandler)
	slog.Info("Registered public service", "path", publicProgramEventTypePath)

	publicProgramEventSeriesPath, publicProgramEventSeriesHandler := openv1connect.NewProgramEventSeriesServiceHandler(programeventpublic.NewSeriesConnectHandler(publicProgramEvents), publicHandlerOpts...)
	mux.Handle(publicProgramEventSeriesPath, publicProgramEventSeriesHandler)
	slog.Info("Registered public service", "path", publicProgramEventSeriesPath)

	publicProgramEventPath, publicProgramEventHandler := openv1connect.NewProgramEventServiceHandler(programeventpublic.NewConnectHandler(publicProgramEvents), publicHandlerOpts...)
	mux.Handle(publicProgramEventPath, publicProgramEventHandler)
	slog.Info("Registered public service", "path", publicProgramEventPath)

	publicShareLinkService, err := sharelinkpublic.NewService(sharelinkpublic.Deps{Units: sharelinkpostgres.NewUnits(db), Targets: sharelinkadapter.NewPublicTargetResolver(db)})
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
