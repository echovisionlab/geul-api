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
	releaseadapter "github.com/echovisionlab/geul-api/internal/adapters/release"
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

func (m musicServiceRegistration) registerArtist() (*artist.ArtistService, *artist.InternalArtistService) {
	deps := m.dependencies
	artistRuntime := artistadapter.NewRuntime(deps.cfg.CDNURL, deps.og.refresher)
	artistDependencies := artist.Dependencies{
		Translation: artistadapter.NewTranslation(),
		Members:     artistadapter.NewMemberProjection(deps.db, deps.cfg.CDNURL),
		Runtime:     artistRuntime,
	}
	artistService := artist.NewAuditedArtistService(
		deps.db, deps.spicedbClient, deps.kratosClient, m.files, deps.servicePublisher, deps.telemetryWriter,
		artistDependencies,
		artist.WithArtistContentBlockStore(deps.contentBlockStore),
	)
	m.registerManage(managev1connect.NewArtistServiceHandler(artistService, m.manageOptions...))

	internalArtistService := artist.NewAuditedInternalArtistService(
		deps.db, deps.servicePublisher, deps.spicedbClient, deps.telemetryWriter,
		artistDependencies,
		artist.WithInternalArtistContentBlockStore(deps.contentBlockStore),
		artist.WithInternalArtistCheckpoints(m.checkpoints),
	)
	m.registerInternal(intrav1connect.NewInternalArtistServiceHandler(internalArtistService, m.internalOptions...))

	publicArtistService := artistpublic.NewArtistService(
		deps.db, deps.spicedbClient, artistadapter.NewPublicMedia(deps.db, artistRuntime),
		artistpublic.WithArtistContentBlockStore(deps.contentBlockStore),
	)
	m.registerPublic(openv1connect.NewArtistServiceHandler(publicArtistService, m.publicOptions...))

	return artistService, internalArtistService
}

func (m musicServiceRegistration) registerLabel() *label.LabelService {
	deps := m.dependencies
	labelRuntime := labeladapter.NewRuntime(deps.cfg.CDNURL, deps.og.refresher)
	labelDependencies := label.Dependencies{
		Translation: labeladapter.NewTranslation(),
		Members:     labeladapter.NewMemberProjection(deps.db, deps.cfg.CDNURL),
		Runtime:     labelRuntime,
	}
	labelService := label.NewAuditedLabelService(
		deps.db, deps.spicedbClient, deps.kratosClient, m.files, deps.servicePublisher, deps.telemetryWriter,
		labelDependencies,
		label.WithLabelContentBlockStore(deps.contentBlockStore),
	)
	m.registerManage(managev1connect.NewLabelServiceHandler(labelService, m.manageOptions...))

	internalLabelService := label.NewAuditedInternalLabelService(
		deps.db, deps.servicePublisher, deps.spicedbClient, deps.telemetryWriter,
		labelDependencies,
		label.WithInternalLabelContentBlockStore(deps.contentBlockStore),
		label.WithInternalLabelCheckpoints(m.checkpoints),
	)
	m.registerInternal(intrav1connect.NewInternalLabelServiceHandler(internalLabelService, m.internalOptions...))

	labelPublicRuntime := labeladapter.NewPublicRuntime(deps.db, deps.cfg.CDNURL, deps.spicedbClient, deps.contentBlockStore)
	publicLabelService := labelpublic.NewLabelService(
		deps.db,
		labelpublic.Dependencies{
			Content: labelPublicRuntime,
			Draft:   labelPublicRuntime,
			Media:   labelPublicRuntime,
		},
	)
	m.registerPublic(openv1connect.NewLabelServiceHandler(publicLabelService, m.publicOptions...))

	return labelService
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
