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
	artistpostgres "github.com/echovisionlab/geul-api/internal/artist/postgres"
	artistpublic "github.com/echovisionlab/geul-api/internal/artist/public"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/label"
	labelpostgres "github.com/echovisionlab/geul-api/internal/label/postgres"
	labelpublic "github.com/echovisionlab/geul-api/internal/label/public"
	labelpublicpostgres "github.com/echovisionlab/geul-api/internal/label/public/postgres"
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
	files           *filemedia.Service
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
	artistUnits, err := artistpostgres.NewUnits(deps.db, artistpostgres.Deps{
		ContentBlocks: deps.contentBlockStore, Publisher: deps.servicePublisher, Checkpoints: m.checkpoints,
		Translation:   artistadapter.NewTranslation(),
		Runtime:       artistRuntime,
		Labels:        labeladapter.NewArtistLinks(),
		Releases:      releaseadapter.NewArtistRelations(),
		ProgramEvents: programeventadapter.NewArtistRelations(),
		Works:         workadapter.NewArtistRelations(),
		ShareLinks:    sharelinkadapter.NewCatalogLinks(),
	})
	if err != nil {
		return nil, fmt.Errorf("initialize artist store: %w", err)
	}
	artistService, err := artist.NewService(artist.Deps{
		Units: artistUnits, SpiceDB: deps.spicedbClient, Fence: identitystate.PostgresFence{}, Audit: deps.telemetryWriter,
		Members: artistadapter.NewMemberProjection(deps.db, deps.cfg.CDNURL),
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
	labelUnits, err := labelpostgres.NewUnits(deps.db, labelpostgres.Deps{
		ContentBlocks: deps.contentBlockStore, Publisher: deps.servicePublisher, Checkpoints: m.checkpoints,
		Translation:   labeladapter.NewTranslation(),
		Runtime:       labeladapter.NewRuntime(deps.cfg.CDNURL, deps.og.refresher),
		Artists:       artistadapter.NewLabelRelations(),
		Releases:      releaseadapter.NewLabelRelations(),
		ProgramEvents: programeventadapter.NewLabelRelations(),
		ShareLinks:    sharelinkadapter.NewCatalogLinks(),
	})
	if err != nil {
		return nil, fmt.Errorf("initialize label store: %w", err)
	}
	labelService, err := label.NewService(label.Deps{
		Units: labelUnits, SpiceDB: deps.spicedbClient, Fence: identitystate.PostgresFence{}, Audit: deps.telemetryWriter,
		Members: labeladapter.NewMemberProjection(deps.db, deps.cfg.CDNURL),
	})
	if err != nil {
		return nil, fmt.Errorf("initialize label service: %w", err)
	}
	m.registerManage(managev1connect.NewLabelServiceHandler(label.NewConnectHandler(labelService), m.manageOptions...))
	m.registerInternal(intrav1connect.NewInternalLabelServiceHandler(label.NewInternalConnectHandler(labelService), m.internalOptions...))

	labelPublicRuntime := labeladapter.NewPublicRuntime(deps.db, deps.cfg.CDNURL, deps.spicedbClient, deps.contentBlockStore)
	publicLabelUnits, err := labelpublicpostgres.NewUnits(deps.db, labelPublicRuntime, labelPublicRuntime)
	if err != nil {
		return nil, fmt.Errorf("initialize public label store: %w", err)
	}
	publicLabelService, err := labelpublic.NewService(labelpublic.Deps{Units: publicLabelUnits, Draft: labelPublicRuntime})
	if err != nil {
		return nil, fmt.Errorf("initialize public label service: %w", err)
	}
	m.registerPublic(openv1connect.NewLabelServiceHandler(labelpublic.NewConnectHandler(publicLabelService), m.publicOptions...))

	return labelService, nil
}

func (m musicServiceRegistration) registerRelease() (*releasepkg.Service, error) {
	deps := m.dependencies
	trackFileManager := filemediaadapter.NewTrackFileManager(m.files)
	artistSummaries := releaseadapter.NewArtistSummaries(deps.db)
	memberSummaries := releaseadapter.NewMemberSummaries(deps.db, deps.cfg.CDNURL)
	labelSummaries := releaseadapter.NewLabelSummaries(deps.db)
	releaseService, err := releasepkg.NewService(releasepkg.Deps{
		DB: deps.db, SpiceDB: deps.spicedbClient, Audit: deps.telemetryWriter, ContentBlocks: deps.contentBlockStore,
		Checkpoints: m.checkpoints, ContentSignals: deps.servicePublisher,
		TrackFiles:   releaseadapter.NewTrackFiles(trackFileManager),
		Transcodes:   releaseadapter.NewTrackTranscodes(deps.transcodeTracker),
		WaveformJobs: releaseadapter.NewWaveformJobs(deps.db, deps.transcodeTracker),
		Assets:       releaseadapter.NewAssets(deps.cfg.CDNURL), OG: releaseadapter.NewOG(deps.cfg.CDNURL),
		ContentOG: releaseadapter.NewContentOG(deps.og.refresher), Members: memberSummaries, Artists: artistSummaries,
		Labels: labelSummaries,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize Release service: %w", err)
	}
	m.registerManage(managev1connect.NewReleaseServiceHandler(releasepkg.NewConnectHandler(releaseService), m.manageOptions...))
	m.registerManage(managev1connect.NewTrackServiceHandler(releasepkg.NewTrackConnectHandler(releaseService), m.manageOptions...))
	m.registerInternal(intrav1connect.NewInternalReleaseServiceHandler(releasepkg.NewInternalConnectHandler(releaseService), m.internalOptions...))

	publicReleaseService, err := publicrelease.NewService(publicrelease.Deps{
		DB: deps.db, SpiceDB: deps.spicedbClient, ContentBlocks: deps.contentBlockStore,
		Media: releaseadapter.NewPublicMedia(
			deps.cfg.CDNURL, deps.cfg.MediaURL, deps.cfg.MediaSigningSecret, m.downloadTTL,
		),
		Downloads: releaseadapter.NewDownloadAccess(deps.db, deps.spicedbClient, mediaassetadapter.NewSegmentConfigs()),
		Members:   memberSummaries, Artists: artistSummaries, Labels: labelSummaries,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize public Release service: %w", err)
	}
	m.registerPublic(openv1connect.NewReleaseServiceHandler(publicrelease.NewConnectHandler(publicReleaseService), m.publicOptions...))

	return releaseService, nil
}

func (m musicServiceRegistration) registerTaxonomy(catalog *referencecatalog.Service) {
	m.registerManage(managev1connect.NewGenreServiceHandler(referencecatalog.NewGenreConnectHandler(catalog), m.manageOptions...))
	m.registerManage(managev1connect.NewStyleServiceHandler(referencecatalog.NewStyleConnectHandler(catalog), m.manageOptions...))
	m.registerManage(managev1connect.NewFormatServiceHandler(referencecatalog.NewFormatConnectHandler(catalog), m.manageOptions...))
}
