package main

import (
	"log/slog"
	"time"

	campaignadapter "github.com/echovisionlab/geul-api/internal/adapters/campaign"
	collaborationadapter "github.com/echovisionlab/geul-api/internal/adapters/collaboration"
	emailauthoringadapter "github.com/echovisionlab/geul-api/internal/adapters/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/adapters/filemediacontent"
	formadapter "github.com/echovisionlab/geul-api/internal/adapters/form"
	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	pageadapter "github.com/echovisionlab/geul-api/internal/adapters/page"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/artist"
	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	filepublic "github.com/echovisionlab/geul-api/internal/filemedia/public"
	formdomain "github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/label"
	"github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/maptheme"
	"github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/menu"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/page"
	"github.com/echovisionlab/geul-api/internal/programevent"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	"github.com/echovisionlab/geul-api/internal/release"
	"github.com/echovisionlab/geul-api/internal/series"
	"github.com/echovisionlab/geul-api/internal/sitesettings"
	translationapplication "github.com/echovisionlab/geul-api/internal/translation/application"
	"github.com/echovisionlab/geul-api/internal/work"
	"github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

type registeredMusicApplications struct {
	artist     *artist.Service
	label      *label.Service
	release    *release.Service
	references *referencecatalog.Service
}

func (r serviceRegistration) registerMusicApplications(fileService *filemedia.Service, collaborationRuntime *collaborationadapter.Runtime, downloadTTL time.Duration) (registeredMusicApplications, error) {
	deps := r.dependencies
	music := musicServiceRegistration{
		dependencies: deps, files: fileService, checkpoints: collaborationRuntime.Checkpoints,
		manageOptions: r.manageOptions, internalOptions: r.internalOptions, publicOptions: r.publicOptions,
		internalTrust: r.internalTrust, downloadTTL: downloadTTL,
	}
	artistService, err := music.registerArtist()
	if err != nil {
		return registeredMusicApplications{}, err
	}
	labelService, err := music.registerLabel()
	if err != nil {
		return registeredMusicApplications{}, err
	}
	releaseService, err := music.registerRelease()
	if err != nil {
		return registeredMusicApplications{}, err
	}
	referenceCatalog, err := r.registerReferenceCatalog()
	if err != nil {
		return registeredMusicApplications{}, err
	}

	music.registerTaxonomy(referenceCatalog)

	return registeredMusicApplications{artist: artistService, label: labelService, release: releaseService, references: referenceCatalog}, nil
}

type contentApplicationRegistration struct {
	registration             serviceRegistration
	publicFiles              *filepublic.Service
	files                    *filemedia.Service
	media                    *filemediacontent.Media
	workRuntime              *workadapter.Runtime
	pageRuntime              *pageadapter.Runtime
	collaboration            *collaborationadapter.Runtime
	formAssets               *formadapter.Assets
	references               *referencecatalog.Service
	legalRuntime             *legaladapter.Runtime
	campaignDelivery         *campaignadapter.DeliveryDispatcher
	emailAuthoringReferences *emailauthoringadapter.CampaignDeliveryReferences
}

type registeredContentDocuments struct {
	work         *work.Service
	post         registeredPost
	programEvent *programevent.Service
	page         *page.Service
	menu         *menu.Service
	form         *formdomain.Service
}

func (c contentApplicationRegistration) registerDocuments() (registeredContentDocuments, error) {
	r := c.registration
	mux := r.dependencies.mux
	fileService, publicFileService, fileContentMedia := c.files, c.publicFiles, c.media
	workRuntime, pageRuntime := c.workRuntime, c.pageRuntime
	collaborationRuntime, formAssets, referenceCatalog := c.collaboration, c.formAssets, c.references
	workService, err := r.registerWork(fileContentMedia, workRuntime, collaborationRuntime)
	if err != nil {
		return registeredContentDocuments{}, err
	}

	// Phase 2: CMS Services
	postComposition, err := r.registerPost(publicFileService, fileService, fileContentMedia)
	if err != nil {
		return registeredContentDocuments{}, err
	}

	categoryPath, categoryHandler := managev1connect.NewCategoryServiceHandler(referencecatalog.NewCategoryConnectHandler(referenceCatalog), r.manageOptions...)
	mux.Handle(categoryPath, categoryHandler)
	slog.Info("Registered service", "path", categoryPath)

	programEventService, err := r.registerProgramEvent(fileContentMedia, collaborationRuntime)
	if err != nil {
		return registeredContentDocuments{}, err
	}

	tagPath, tagHandler := managev1connect.NewTagServiceHandler(referencecatalog.NewTagConnectHandler(referenceCatalog), r.manageOptions...)
	mux.Handle(tagPath, tagHandler)
	slog.Info("Registered service", "path", tagPath)

	pageService, err := r.registerPage(fileService, fileContentMedia, pageRuntime)
	if err != nil {
		return registeredContentDocuments{}, err
	}

	menuService, err := r.registerMenu(collaborationRuntime)
	if err != nil {
		return registeredContentDocuments{}, err
	}

	formService, err := r.registerForm(formAssets)
	if err != nil {
		return registeredContentDocuments{}, err
	}

	return registeredContentDocuments{work: workService, post: postComposition, programEvent: programEventService, page: pageService, menu: menuService, form: formService}, nil
}

type registeredContentSupport struct {
	series              *series.Service
	legalUnits          mutation.UnitOfWork[legal.Store]
	legalPolicies       *legal.Service
	mapTheme            *maptheme.Service
	emailAuthoringUnits mutation.UnitOfWork[emailauthoring.Store]
	mailAdapters        *emaildelivery.Service
	campaignUnits       campaign.Units
	siteSettings        *sitesettings.Service
}

func (c contentApplicationRegistration) registerSupport(postComposition registeredPost) (registeredContentSupport, error) {
	r := c.registration
	mux := r.dependencies.mux
	collaborationRuntime, referenceCatalog := c.collaboration, c.references
	legalRuntime, campaignDeliveryDispatcher := c.legalRuntime, c.campaignDelivery
	emailAuthoringReferences := c.emailAuthoringReferences
	// Phase 3: Reference Data Services
	seriesService, err := r.registerSeries(collaborationRuntime)
	if err != nil {
		return registeredContentSupport{}, err
	}

	clientPath, clientHandler := managev1connect.NewClientServiceHandler(referencecatalog.NewClientConnectHandler(referenceCatalog), r.manageOptions...)
	mux.Handle(clientPath, clientHandler)
	slog.Info("Registered service", "path", clientPath)

	legalUnits, legalPolicyService, err := r.registerLegal(legalRuntime, campaignDeliveryDispatcher, collaborationRuntime)
	if err != nil {
		return registeredContentSupport{}, err
	}

	mapPlacePath, mapPlaceHandler := managev1connect.NewMapPlaceServiceHandler(referencecatalog.NewMapPlaceConnectHandler(referenceCatalog), r.manageOptions...)
	mux.Handle(mapPlacePath, mapPlaceHandler)
	slog.Info("Registered service", "path", mapPlacePath)

	mapThemeService, err := r.registerMapTheme()
	if err != nil {
		return registeredContentSupport{}, err
	}

	emailAuthoringUnits, err := r.registerEmailAuthoring(collaborationRuntime, emailAuthoringReferences)
	if err != nil {
		return registeredContentSupport{}, err
	}

	mailAdapterService, err := r.registerEmailDelivery()
	if err != nil {
		return registeredContentSupport{}, err
	}

	if err := r.registerComments(postComposition.members, postComposition.units); err != nil {
		return registeredContentSupport{}, err
	}

	campaignUnits, err := r.registerCampaign(collaborationRuntime)
	if err != nil {
		return registeredContentSupport{}, err
	}

	siteSettingService, err := r.registerSiteAndAdministration()
	if err != nil {
		return registeredContentSupport{}, err
	}

	return registeredContentSupport{
		series: seriesService, legalUnits: legalUnits, legalPolicies: legalPolicyService,
		mapTheme: mapThemeService, emailAuthoringUnits: emailAuthoringUnits, mailAdapters: mailAdapterService,
		campaignUnits: campaignUnits, siteSettings: siteSettingService,
	}, nil
}

func (r serviceRegistration) registerAccountRoutes(accountComposition registeredAccounts, accountHooks registeredAccountHooks) {
	mux := r.dependencies.mux
	memberPath, memberHandler := managev1connect.NewMemberServiceHandler(member.NewConnectHandler(accountComposition.members), r.manageOptions...)
	mux.Handle(memberPath, memberHandler)
	slog.Info("Registered service", "path", memberPath)
	accountPath, accountHandler := managev1connect.NewAccountServiceHandler(accountHooks.account, r.manageOptions...)
	mux.Handle(accountPath, accountHandler)
	slog.Info("Registered service", "path", accountPath)
}

func (r serviceRegistration) registerInternalOGRoutes() {
	mux, ogDeps := r.dependencies.mux, r.dependencies.og
	internalOgService := og.NewInternalConnectHandler(og.NewLifecycle(ogDeps.units))
	internalOgPath, internalOgHandler := intrav1connect.NewInternalOgServiceHandler(internalOgService, r.internalOptions...)
	mux.Handle(internalOgPath, r.internalTrust.og(internalOgHandler))
	slog.Info("Registered internal service", "path", internalOgPath)
}

func (c contentApplicationRegistration) registerAIDocumentApplications(music registeredMusicApplications, documents registeredContentDocuments, support registeredContentSupport, internal internalContentServices, translations *translationapplication.Service, members *member.Service) error {
	return c.registration.registerAIDocuments(aiDocumentApplications{
		post:           documents.post.service,
		page:           documents.page,
		work:           documents.work,
		programEvent:   documents.programEvent,
		release:        music.release,
		artist:         music.artist,
		label:          music.label,
		menu:           documents.menu,
		emailAuthoring: internal.emailAuthoring,
		campaign:       internal.campaign,
		form:           documents.form,
		legal:          internal.legalDocuments,
		series:         support.series,
	}, contentMCPApplications{
		legalPolicies: support.legalPolicies,
		categories:    music.references,
		tags:          music.references,
		clients:       music.references,
		mapPlaces:     music.references,
		members:       members,
		artists:       music.artist,
		files:         c.files,
		eventTypes:    documents.programEvent,
		eventSeries:   documents.programEvent,
		labels:        music.label,
		genres:        music.references,
		styles:        music.references,
		formats:       music.references,
		forms:         documents.form,
		postSeries:    support.series,
		tracks:        music.release,
		mapThemes:     support.mapTheme,
		menus:         documents.menu,
		menuSettings:  support.siteSettings,
		siteSettings:  support.siteSettings,
		mailAdapters:  support.mailAdapters,
	}, translations, c.files)
}

func (c contentApplicationRegistration) registerPublic(accounts registeredAccounts, post registeredPost) error {
	return (publicServiceRegistration{
		dependencies:   c.registration.dependencies,
		handlerOptions: c.registration.publicOptions,
		fileService:    c.publicFiles,
		postShareLinks: post.shareLinks,
		postMembers:    post.members,
		workRuntime:    c.workRuntime,
		legalRuntime:   c.legalRuntime,
		formAssets:     c.formAssets,
		accounts:       accounts.account,
	}).register()
}
