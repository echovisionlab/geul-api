package main

import (
	"fmt"
	"log/slog"

	artistadapter "github.com/echovisionlab/geul-api/internal/adapters/artist"
	collaborationadapter "github.com/echovisionlab/geul-api/internal/adapters/collaboration"
	"github.com/echovisionlab/geul-api/internal/adapters/filemediacontent"
	formadapter "github.com/echovisionlab/geul-api/internal/adapters/form"
	formogadapter "github.com/echovisionlab/geul-api/internal/adapters/form/og"
	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	menuadapter "github.com/echovisionlab/geul-api/internal/adapters/menu"
	pageadapter "github.com/echovisionlab/geul-api/internal/adapters/page"
	pageruntime "github.com/echovisionlab/geul-api/internal/adapters/page/runtime"
	postadapter "github.com/echovisionlab/geul-api/internal/adapters/post"
	postruntime "github.com/echovisionlab/geul-api/internal/adapters/post/runtime"
	programeventadapter "github.com/echovisionlab/geul-api/internal/adapters/programevent"
	seriesadapter "github.com/echovisionlab/geul-api/internal/adapters/series"
	sharelinkadapter "github.com/echovisionlab/geul-api/internal/adapters/sharelink"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	filepublic "github.com/echovisionlab/geul-api/internal/filemedia/public"
	formdomain "github.com/echovisionlab/geul-api/internal/form"
	formpostgres "github.com/echovisionlab/geul-api/internal/form/postgres"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/menu"
	menupostgres "github.com/echovisionlab/geul-api/internal/menu/postgres"
	mutation "github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/page"
	pagepostgres "github.com/echovisionlab/geul-api/internal/page/postgres"
	"github.com/echovisionlab/geul-api/internal/post"
	postpostgres "github.com/echovisionlab/geul-api/internal/post/postgres"
	"github.com/echovisionlab/geul-api/internal/programevent"
	programeventpostgres "github.com/echovisionlab/geul-api/internal/programevent/postgres"
	"github.com/echovisionlab/geul-api/internal/series"
	seriespostgres "github.com/echovisionlab/geul-api/internal/series/postgres"
	"github.com/echovisionlab/geul-api/internal/sharelink"
	"github.com/echovisionlab/geul-api/internal/work"
	workpostgres "github.com/echovisionlab/geul-api/internal/work/postgres"
	"github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

func (r serviceRegistration) registerWork(fileContentMedia *filemediacontent.Media, workRuntime *workadapter.Runtime, collaborationRuntime *collaborationadapter.Runtime) (*work.Service, error) {
	deps := r.dependencies
	workUnits, err := workpostgres.NewUnits(deps.db, workpostgres.Ports{
		ContentBlocks: deps.contentBlockStore, Signals: deps.servicePublisher, Checkpoints: collaborationRuntime.Checkpoints,
		Runtime: workRuntime, Media: fileContentMedia, ShareLinks: sharelinkadapter.EntityLinks{Type: sharelink.EntityWork},
	})
	if err != nil {
		return nil, fmt.Errorf("initialize Work store: %w", err)
	}
	workService, err := work.NewService(work.Deps{
		Units: workUnits, SpiceDB: deps.spicedbClient, Fence: identitystate.PostgresFence{}, Audit: deps.telemetryWriter,
		Members: workadapter.NewMemberSummaries(deps.db, deps.cfg.CDNURL),
	})
	if err != nil {
		return nil, fmt.Errorf("initialize Work service: %w", err)
	}
	workPath, workHandler := managev1connect.NewWorkServiceHandler(work.NewConnectHandler(workService), r.manageOptions...)
	deps.mux.Handle(workPath, workHandler)
	slog.Info("Registered service", "path", workPath)
	internalWorkPath, internalWorkHandler := intrav1connect.NewInternalWorkServiceHandler(work.NewInternalConnectHandler(workService), r.internalOptions...)
	deps.mux.Handle(internalWorkPath, r.internalTrust.collab(internalWorkHandler))
	slog.Info("Registered internal service", "path", internalWorkPath)

	return workService, nil
}

type registeredPost struct {
	members    *postadapter.MemberSummaries
	shareLinks postruntime.ShareLinks
	units      mutation.UnitOfWork[post.Store]
	service    *post.Service
}

func (r serviceRegistration) registerPost(publicFileService *filepublic.Service, fileService *filemedia.Service, fileContentMedia *filemediacontent.Media) (registeredPost, error) {
	deps := r.dependencies
	postMembers := postadapter.NewMemberSummaries(deps.db, deps.cfg.CDNURL)
	postShareLinks := postruntime.ShareLinks{}
	postMedia := postruntime.ContentBlockMedia{}
	postFiles := postadapter.NewFiles(fileService, publicFileService)
	postVersionRestore := postruntime.VersionRestore{}
	postUnits, err := postpostgres.NewUnits(deps.db, postpostgres.Ports{
		ContentBlocks: deps.contentBlockStore, Signals: deps.servicePublisher, OG: deps.og.refresher,
		ShareLinkCleanup: sharelinkadapter.EntityLinks{Type: sharelink.EntityPost},
		MediaLoader:      postMedia, Media: fileContentMedia, VersionRestore: postVersionRestore, CDNDomain: deps.cfg.CDNURL,
	})
	if err != nil {
		return registeredPost{}, fmt.Errorf("initialize Post store: %w", err)
	}
	postService, err := post.NewService(post.Deps{
		Units: postUnits, SpiceDB: deps.spicedbClient, Fence: identitystate.PostgresFence{}, Audit: deps.telemetryWriter,
		Files: postFiles, Members: postMembers,
	})
	if err != nil {
		return registeredPost{}, fmt.Errorf("initialize Post service: %w", err)
	}
	postPath, postHandler := managev1connect.NewPostServiceHandler(post.NewConnectHandler(postService), r.manageOptions...)
	deps.mux.Handle(postPath, postHandler)
	slog.Info("Registered service", "path", postPath)
	internalPostPath, internalPostHandler := intrav1connect.NewInternalPostServiceHandler(post.NewInternalConnectHandler(postService), r.internalOptions...)
	deps.mux.Handle(internalPostPath, r.internalTrust.collab(internalPostHandler))
	slog.Info("Registered internal service", "path", internalPostPath)

	return registeredPost{members: postMembers, shareLinks: postShareLinks, units: postUnits, service: postService}, nil
}

func (r serviceRegistration) registerProgramEvent(fileContentMedia *filemediacontent.Media, collaborationRuntime *collaborationadapter.Runtime) (*programevent.Service, error) {
	deps := r.dependencies
	programEventUnits, err := programeventpostgres.NewUnits(deps.db, programeventpostgres.Ports{
		ContentBlocks: deps.contentBlockStore, Signals: deps.servicePublisher, Media: programeventadapter.NewRuntime(deps.cfg.CDNURL),
		Files: programeventadapter.NewFiles(fileContentMedia), Members: memberadapter.ContentMembers{},
		CreditArtists: artistadapter.CreditArtists{}, Checkpoints: collaborationRuntime.Checkpoints,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize program event store: %w", err)
	}
	programEventService, err := programevent.NewService(programevent.Deps{
		Units: programEventUnits, SpiceDB: deps.spicedbClient, Fence: identitystate.PostgresFence{}, Audit: deps.telemetryWriter,
		CreditMembers: programeventadapter.NewCreditMemberSummaries(deps.db, deps.cfg.CDNURL),
	})
	if err != nil {
		return nil, fmt.Errorf("initialize program event service: %w", err)
	}
	programEventTypePath, programEventTypeHandler := managev1connect.NewProgramEventTypeServiceHandler(programevent.NewTypeConnectHandler(programEventService), r.manageOptions...)
	deps.mux.Handle(programEventTypePath, programEventTypeHandler)
	slog.Info("Registered service", "path", programEventTypePath)

	programEventSeriesPath, programEventSeriesHandler := managev1connect.NewProgramEventSeriesServiceHandler(programevent.NewSeriesConnectHandler(programEventService), r.manageOptions...)
	deps.mux.Handle(programEventSeriesPath, programEventSeriesHandler)
	slog.Info("Registered service", "path", programEventSeriesPath)

	programEventPath, programEventHandler := managev1connect.NewProgramEventServiceHandler(programevent.NewConnectHandler(programEventService), r.manageOptions...)
	deps.mux.Handle(programEventPath, programEventHandler)
	slog.Info("Registered service", "path", programEventPath)

	return programEventService, nil
}

func (r serviceRegistration) registerPage(fileService *filemedia.Service, fileContentMedia *filemediacontent.Media, pageRuntime *pageadapter.Runtime) (*page.Service, error) {
	deps := r.dependencies
	pageUnits, err := pagepostgres.NewUnits(deps.db, pagepostgres.Ports{
		ContentBlocks: deps.contentBlockStore, Signals: deps.servicePublisher, Runtime: pageRuntime, MediaHydrator: fileContentMedia,
		MenuTargets: menuadapter.NewTargetLifecycle(deps.telemetryWriter), Members: memberadapter.PageMembers{},
		ShareLinks: sharelinkadapter.PageLinks{},
	})
	if err != nil {
		return nil, fmt.Errorf("initialize Page store: %w", err)
	}
	pageService, err := page.NewService(page.Deps{
		Units: pageUnits, SpiceDB: deps.spicedbClient, Fence: identitystate.PostgresFence{}, Audit: deps.telemetryWriter,
		Files: pageruntime.NewFiles(fileService),
	})
	if err != nil {
		return nil, fmt.Errorf("initialize Page service: %w", err)
	}
	pagePath, pageHandler := managev1connect.NewPageServiceHandler(page.NewConnectHandler(pageService), r.manageOptions...)
	deps.mux.Handle(pagePath, pageHandler)
	slog.Info("Registered service", "path", pagePath)
	internalPagePath, internalPageHandler := intrav1connect.NewInternalPageServiceHandler(
		page.NewInternalConnectHandler(pageService), r.internalOptions...,
	)
	deps.mux.Handle(internalPagePath, r.internalTrust.collab(internalPageHandler))
	slog.Info("Registered internal service", "path", internalPagePath)

	return pageService, nil
}

func (r serviceRegistration) registerMenu(collaborationRuntime *collaborationadapter.Runtime) (*menu.Service, error) {
	deps := r.dependencies
	menuService, err := menu.NewService(menu.Deps{
		Units: menupostgres.NewUnits(deps.db, menupostgres.Ports{
			SiteSettings: menuadapter.NewSiteSettingsReferences(deps.telemetryWriter),
			Checkpoints:  collaborationRuntime.Checkpoints,
		}),
		SpiceDB: deps.spicedbClient, Fence: identitystate.PostgresFence{}, Audit: deps.telemetryWriter,
	})
	if err != nil {
		return nil, err
	}
	menuPath, menuHandler := managev1connect.NewMenuServiceHandler(menu.NewConnectHandler(menuService), r.manageOptions...)
	deps.mux.Handle(menuPath, menuHandler)
	slog.Info("Registered service", "path", menuPath)
	internalMenuPath, internalMenuHandler := intrav1connect.NewInternalMenuServiceHandler(
		menu.NewInternalConnectHandler(menuService), r.internalOptions...,
	)
	deps.mux.Handle(internalMenuPath, r.internalTrust.collab(internalMenuHandler))
	slog.Info("Registered internal service", "path", internalMenuPath)

	return menuService, nil
}

func (r serviceRegistration) registerForm(formAssets *formadapter.Assets) (*formdomain.Service, error) {
	deps := r.dependencies
	formUnits, err := formpostgres.NewUnits(deps.db, formpostgres.Ports{
		ContentBlocks: deps.contentBlockStore, Signals: deps.servicePublisher, Assets: formAssets,
		OG: formogadapter.NewOG(deps.cfg.CDNURL, deps.og.refresher), Routes: formadapter.NewRoutes(),
		Collaboration: formadapter.NewCollaboration(deps.spicedbClient), Members: memberadapter.PreferredLocales{},
		ShareLinks: sharelinkadapter.FormLinks{},
	})
	if err != nil {
		return nil, fmt.Errorf("initialize Form store: %w", err)
	}
	formService, err := formdomain.NewService(formdomain.Deps{
		Units: formUnits, SpiceDB: deps.spicedbClient, Fence: identitystate.PostgresFence{}, Audit: deps.telemetryWriter,
		Password: deps.passwordHasher,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize Form service: %w", err)
	}
	formPath, formHandler := managev1connect.NewFormServiceHandler(formdomain.NewConnectHandler(formService), r.manageOptions...)
	deps.mux.Handle(formPath, formHandler)
	slog.Info("Registered service", "path", formPath)
	internalFormPath, internalFormHandler := intrav1connect.NewInternalFormServiceHandler(
		formdomain.NewInternalConnectHandler(formService), r.internalOptions...,
	)
	deps.mux.Handle(internalFormPath, r.internalTrust.collab(internalFormHandler))
	slog.Info("Registered internal service", "path", internalFormPath)

	return formService, nil
}

func (r serviceRegistration) registerSeries(collaborationRuntime *collaborationadapter.Runtime) (*series.Service, error) {
	deps := r.dependencies
	seriesRuntime := seriesadapter.NewRuntime(deps.db, deps.cfg.CDNURL, deps.og.refresher)
	seriesMenuTargets := menuadapter.NewTargetLifecycle(deps.telemetryWriter)
	seriesPostAccess := seriesadapter.PostAccess{}
	seriesUnits, err := seriespostgres.NewUnits(deps.db, seriespostgres.Collaborators{
		Media: seriesRuntime, MenuTargets: seriesMenuTargets, Posts: postadapter.SeriesMembership{},
		PostAccess: seriesPostAccess, SpiceDB: deps.spicedbClient, Checkpoints: collaborationRuntime.Checkpoints,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize Post Series store: %w", err)
	}
	seriesLocales, err := seriespostgres.NewLocales(deps.db)
	if err != nil {
		return nil, fmt.Errorf("initialize Post Series locales: %w", err)
	}
	seriesService, err := series.NewService(series.Deps{
		Units: seriesUnits, SpiceDB: deps.spicedbClient, Fence: identitystate.PostgresFence{}, Audit: deps.telemetryWriter,
		Media: seriesRuntime, Reads: seriesRuntime, Members: seriesadapter.NewMemberSummaries(deps.db, deps.cfg.CDNURL),
		Locales: seriesLocales,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize Post Series service: %w", err)
	}
	seriesPath, seriesHandler := managev1connect.NewSeriesServiceHandler(series.NewConnectHandler(seriesService), r.manageOptions...)
	deps.mux.Handle(seriesPath, seriesHandler)
	slog.Info("Registered service", "path", seriesPath)
	internalPostSeriesPath, internalPostSeriesHandler := intrav1connect.NewInternalPostSeriesServiceHandler(
		series.NewInternalConnectHandler(seriesService), r.internalOptions...,
	)
	deps.mux.Handle(internalPostSeriesPath, r.internalTrust.collab(internalPostSeriesHandler))
	slog.Info("Registered internal service", "path", internalPostSeriesPath)

	return seriesService, nil
}

func (r serviceRegistration) registerComments(postMembers *postadapter.MemberSummaries, postUnits mutation.UnitOfWork[post.Store]) error {
	deps := r.dependencies
	commentService, err := post.NewCommentService(post.CommentDeps{
		Units: postUnits, SpiceDB: deps.spicedbClient, Fence: identitystate.PostgresFence{}, Members: postMembers, Audit: deps.telemetryWriter,
	})
	if err != nil {
		return fmt.Errorf("initialize Post comment service: %w", err)
	}
	commentPath, commentHandler := managev1connect.NewCommentServiceHandler(post.NewCommentConnectHandler(commentService), r.manageOptions...)
	deps.mux.Handle(commentPath, commentHandler)
	slog.Info("Registered service", "path", commentPath)

	return nil
}
