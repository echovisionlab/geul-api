package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"
	artistadapter "github.com/echovisionlab/geul-api/internal/adapters/artist"
	filemediaadapter "github.com/echovisionlab/geul-api/internal/adapters/filemedia"
	labeladapter "github.com/echovisionlab/geul-api/internal/adapters/label"
	mediaassetadapter "github.com/echovisionlab/geul-api/internal/adapters/mediaasset"
	programeventadapter "github.com/echovisionlab/geul-api/internal/adapters/programevent"
	releaseadapter "github.com/echovisionlab/geul-api/internal/adapters/release"
	sharelinkadapter "github.com/echovisionlab/geul-api/internal/adapters/sharelink"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/artist"
	artistpublic "github.com/echovisionlab/geul-api/internal/artist/public"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/label"
	labelpublic "github.com/echovisionlab/geul-api/internal/label/public"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
	releasepkg "github.com/echovisionlab/geul-api/internal/release"
	publicrelease "github.com/echovisionlab/geul-api/internal/release/public"
	"github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
)

// Keep each music domain's Manage, internal Collab, and public routes together.
// Handler options and trust wrappers are supplied by the main composition root.
type musicServiceRegistration struct {
	dependencies    serviceRegistrationDependencies
	files           *filemedia.FileService
	checkpoints     persistencecheckpoint.ContributorFence
	manageOptions   []connect.HandlerOption
	internalOptions []connect.HandlerOption
	publicOptions   []connect.HandlerOption
	internalTrust   internalRPCTrustBoundary
	downloadTTL     time.Duration
}

func (m musicServiceRegistration) registerManage(path string, handler http.Handler) {
	m.dependencies.mux.Handle(path, handler)
	slog.Info("Registered service", "path", path)
}
func (m musicServiceRegistration) registerInternal(path string, handler http.Handler) {
	m.dependencies.mux.Handle(path, m.internalTrust.collab(handler))
	slog.Info("Registered internal service", "path", path)
}
func (m musicServiceRegistration) registerPublic(path string, handler http.Handler) {
	m.dependencies.mux.Handle(path, handler)
	slog.Info("Registered public service", "path", path)
}

func (m musicServiceRegistration) registerArtist() (*artist.Service, error) {
	deps := m.dependencies
	artistRuntime := artistadapter.NewRuntime(deps.cfg.CDNURL, deps.og.refresher)
	artistService, err := artist.NewService(artist.Deps{
		DB: deps.db, SpiceDB: deps.spicedbClient, Audit: deps.telemetryWriter, Publisher: deps.servicePublisher,
		ContentBlocks: deps.contentBlockStore, Checkpoints: m.checkpoints,
		Translation:   artistadapter.NewTranslation(),
		Members:       artistadapter.NewMemberProjection(deps.db, deps.cfg.CDNURL),
		Runtime:       artistRuntime,
		Labels:        labeladapter.NewArtistLinks(),
		Releases:      releaseadapter.NewArtistRelations(),
		ProgramEvents: programeventadapter.NewArtistRelations(),
		Works:         workadapter.NewArtistRelations(),
		ShareLinks:    sharelinkadapter.NewCatalogLinks(),
	})
	if err != nil {
		return nil, fmt.Errorf("initialize artist service: %w", err)
	}
	m.registerManage(managev1connect.NewArtistServiceHandler(artist.NewConnectHandler(artistService), m.manageOptions...))
	m.registerInternal(intrav1connect.NewInternalArtistServiceHandler(artist.NewInternalConnectHandler(artistService), m.internalOptions...))

	publicArtistService, err := artistpublic.NewService(artistpublic.Deps{
		DB: deps.db, SpiceDB: deps.spicedbClient, ContentBlocks: deps.contentBlockStore,
		Media: artistadapter.NewPublicMedia(deps.db, artistRuntime),
	})
	if err != nil {
		return nil, fmt.Errorf("initialize public artist service: %w", err)
	}
	m.registerPublic(openv1connect.NewArtistServiceHandler(artistpublic.NewConnectHandler(publicArtistService), m.publicOptions...))
	return artistService, nil
}

func (m musicServiceRegistration) registerLabel() (*label.Service, error) {
	deps := m.dependencies
	labelService, err := label.NewService(label.Deps{
		DB: deps.db, SpiceDB: deps.spicedbClient, Audit: deps.telemetryWriter, Publisher: deps.servicePublisher,
		ContentBlocks: deps.contentBlockStore, Checkpoints: m.checkpoints,
		Translation:   labeladapter.NewTranslation(),
		Members:       labeladapter.NewMemberProjection(deps.db, deps.cfg.CDNURL),
		Runtime:       labeladapter.NewRuntime(deps.cfg.CDNURL, deps.og.refresher),
		Artists:       artistadapter.NewLabelRelations(),
		Releases:      releaseadapter.NewLabelRelations(),
		ProgramEvents: programeventadapter.NewLabelRelations(),
		ShareLinks:    sharelinkadapter.NewCatalogLinks(),
	})
	if err != nil {
		return nil, fmt.Errorf("initialize label service: %w", err)
	}
	m.registerManage(managev1connect.NewLabelServiceHandler(label.NewConnectHandler(labelService), m.manageOptions...))
	m.registerInternal(intrav1connect.NewInternalLabelServiceHandler(label.NewInternalConnectHandler(labelService), m.internalOptions...))

	labelPublicRuntime := labeladapter.NewPublicRuntime(deps.db, deps.cfg.CDNURL, deps.spicedbClient, deps.contentBlockStore)
	publicLabelService, err := labelpublic.NewService(labelpublic.Deps{
		DB: deps.db, Content: labelPublicRuntime, Draft: labelPublicRuntime, Media: labelPublicRuntime,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize public label service: %w", err)
	}
	m.registerPublic(openv1connect.NewLabelServiceHandler(labelpublic.NewConnectHandler(publicLabelService), m.publicOptions...))

	return labelService, nil
}

func (m musicServiceRegistration) registerRelease() (*releasepkg.ReleaseService, *releasepkg.InternalReleaseService, *releasepkg.TrackService) {
	deps := m.dependencies
	trackFileManager := filemediaadapter.NewTrackFileManager(m.files)
	releaseTrackFiles := releaseadapter.NewTrackFiles(trackFileManager)
	releaseAssets := releaseadapter.NewAssets(deps.cfg.CDNURL)
	releaseOG := releaseadapter.NewOG(deps.cfg.CDNURL)
	releaseContentOG := releaseadapter.NewContentOG(deps.og.refresher)
	releaseWaveformJobs := releaseadapter.NewWaveformJobs(deps.db, deps.transcodeTracker)

	releaseService := releasepkg.NewAuditedReleaseService(
		deps.db, deps.spicedbClient, deps.kratosClient, releaseTrackFiles, releaseAssets, releaseOG,
		releaseWaveformJobs, deps.servicePublisher, deps.telemetryWriter,
		releasepkg.WithReleaseContentBlockStore(deps.contentBlockStore),
	)
	m.registerManage(managev1connect.NewReleaseServiceHandler(releaseService, m.manageOptions...))

	trackService := releasepkg.NewAuditedTrackService(
		deps.db,
		releaseadapter.NewTrackTranscodes(deps.transcodeTracker),
		releaseWaveformJobs,
		deps.spicedbClient,
		releaseTrackFiles,
		releaseadapter.NewMemberSummaries(deps.db, deps.cfg.CDNURL),
		releaseadapter.NewArtistSummaries(deps.db),
		deps.telemetryWriter,
	)
	m.registerManage(managev1connect.NewTrackServiceHandler(trackService, m.manageOptions...))

	internalReleaseService := releasepkg.NewAuditedInternalReleaseService(
		deps.db, deps.spicedbClient, deps.telemetryWriter, releaseContentOG, releaseadapter.NewArtistSummaries(deps.db),
		releasepkg.WithInternalReleaseContentBlockStore(deps.contentBlockStore),
		releasepkg.WithInternalReleaseCheckpoints(m.checkpoints),
		releasepkg.WithInternalReleaseAIDocumentPublisher(deps.servicePublisher),
	)
	m.registerInternal(intrav1connect.NewInternalReleaseServiceHandler(internalReleaseService, m.internalOptions...))

	publicReleaseService := publicrelease.NewReleaseService(
		deps.db, deps.spicedbClient, releaseadapter.NewPublicMedia(
			deps.cfg.CDNURL, deps.cfg.MediaURL, deps.cfg.MediaSigningSecret, m.downloadTTL,
		),
		releaseadapter.NewDownloadAccess(deps.db, deps.spicedbClient, mediaassetadapter.NewSegmentConfigs()),
		releaseadapter.NewMemberSummaries(deps.db, deps.cfg.CDNURL),
		releaseadapter.NewArtistSummaries(deps.db),
		releaseadapter.NewLabelSummaries(deps.db),
		publicrelease.WithReleaseContentBlockStore(deps.contentBlockStore),
	)
	m.registerPublic(openv1connect.NewReleaseServiceHandler(publicReleaseService, m.publicOptions...))

	return releaseService, internalReleaseService, trackService
}

func (m musicServiceRegistration) registerTaxonomy() (*referencecatalog.GenreService, *referencecatalog.StyleService, *referencecatalog.FormatService) {
	deps := m.dependencies
	genreService := referencecatalog.NewAuditedGenreService(deps.db, deps.telemetryWriter, deps.spicedbClient)
	m.registerManage(managev1connect.NewGenreServiceHandler(genreService, m.manageOptions...))

	styleService := referencecatalog.NewAuditedStyleService(deps.db, deps.telemetryWriter, deps.spicedbClient)
	m.registerManage(managev1connect.NewStyleServiceHandler(styleService, m.manageOptions...))

	formatService := referencecatalog.NewAuditedFormatService(deps.db, deps.telemetryWriter, deps.spicedbClient)
	m.registerManage(managev1connect.NewFormatServiceHandler(formatService, m.manageOptions...))
	return genreService, styleService, formatService
}
