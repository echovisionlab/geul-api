package main

import (
	filepublic "github.com/echovisionlab/geul-api/internal/filemedia/public"
	filepublicpostgres "github.com/echovisionlab/geul-api/internal/filemedia/public/postgres"

	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/echovisionlab/geul-api/internal/adapters/filemediamembers"
	mediaassetadapter "github.com/echovisionlab/geul-api/internal/adapters/mediaasset"
	"github.com/echovisionlab/geul-api/internal/auth"

	filemediapostgres "github.com/echovisionlab/geul-api/internal/filemedia/postgres"

	collaborationadapter "github.com/echovisionlab/geul-api/internal/adapters/collaboration"
	filemediaadapter "github.com/echovisionlab/geul-api/internal/adapters/filemedia"
	"github.com/echovisionlab/geul-api/internal/adapters/filemediapolicy"
	"github.com/echovisionlab/geul-api/internal/adapters/filemediaqueue"
	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	pageadapter "github.com/echovisionlab/geul-api/internal/adapters/page"
	releaseadapter "github.com/echovisionlab/geul-api/internal/adapters/release"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/collaboration"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/page"
	"github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

func (r serviceRegistration) newFiles(downloadTTL time.Duration, legalRuntime *legaladapter.Runtime) (*filemedia.Service, error) {
	deps := r.dependencies
	filePostAccess := filemediaadapter.NewPostAccess(deps.spicedbClient)
	filePageAccess := pageadapter.NewPolicyAccess(page.NewPolicyAuthority(deps.spicedbClient))
	fileWorkAccess := workadapter.NewPolicyAccess(deps.spicedbClient)
	fileEventAccess := &filemediaadapter.ProgramEventAttachment{}
	fileReleaseAccess := releaseadapter.NewPolicyAccess(deps.spicedbClient)
	fileTrackAttachment := filemediaadapter.NewTrackAttachment(deps.telemetryWriter)
	filePolicyOwners := filemediapolicy.NewPolicyOwners(filemediapolicy.PolicyOwnerPorts{Posts: filePostAccess, Pages: filePageAccess, Works: fileWorkAccess, Events: fileEventAccess, Releases: fileReleaseAccess, SpiceDB: deps.spicedbClient})

	fileMediaPorts := filemediapostgres.Ports{UploadWorks: filemediapolicy.WorkExistence{}, MeshJobs: filemediaqueue.Mesh(deps.transcodeTracker, deps.servicePublisher), IngestTranscodes: filemediaqueue.Transcodes(deps.transcodeTracker, deps.servicePublisher), UploadTracks: fileTrackAttachment, DeletionQueue: filemediaqueue.Async(deps.servicePublisher), PolicyOwners: filePolicyOwners, PolicyAudiences: filemediapolicy.Audiences{}, PolicySignals: filemediaqueue.Policy(deps.servicePublisher)}
	fileService, err := filemedia.NewService(filemedia.Deps{
		IngestLocks:        filemediapostgres.NewIngestLocks(deps.db, fileMediaPorts),
		FaviconCommits:     filemediapostgres.NewFaviconCommits(deps.db, fileMediaPorts),
		Units:              filemediapostgres.NewUnits(deps.db, fileMediaPorts),
		Fence:              identitystate.PostgresFence{},
		SpiceDB:            deps.spicedbClient,
		Audit:              deps.telemetryWriter,
		S3:                 deps.s3Client,
		Presigner:          deps.s3PresignClient,
		AsyncPublisher:     filemediaqueue.Lifecycle(deps.servicePublisher),
		S3Bucket:           deps.cfg.S3Bucket,
		CDNDomain:          deps.cfg.CDNURL,
		MediaDomain:        deps.cfg.MediaURL,
		MediaSecret:        deps.cfg.MediaSigningSecret,
		DownloadTTL:        downloadTTL,
		EditorImageMaxSize: deps.cfg.EditorImageMaxSize,
		LegalRoutes:        legalRuntime,
		MemberSummaries:    filemediamembers.NewMemberSummaries(deps.db, deps.cfg.CDNURL),
	})
	if err != nil {
		return nil, fmt.Errorf("initialize file service: %w", err)
	}
	return fileService, nil
}

func (r serviceRegistration) registerCollaboration() *collaborationadapter.Runtime {
	deps := r.dependencies
	collaborationRuntime := collaborationadapter.NewRuntime(deps.db, deps.spicedbClient, deps.cfg.CDNURL)
	internalCollaborationAuthorizationService := collaboration.NewService(
		deps.db,
		collaborationRuntime.Registry,
		collaborationRuntime.Members,
	)
	internalCollaborationAuthorizationPath, internalCollaborationAuthorizationHandler := intrav1connect.NewInternalCollaborationAuthorizationServiceHandler(
		internalCollaborationAuthorizationService,
		r.internalOptions...,
	)
	deps.mux.Handle(internalCollaborationAuthorizationPath, r.internalTrust.collab(internalCollaborationAuthorizationHandler))
	slog.Info("Registered internal service", "path", internalCollaborationAuthorizationPath)

	return collaborationRuntime
}

func (r serviceRegistration) registerFileRoutes(fileService *filemedia.Service) {
	deps := r.dependencies
	filePath, fileHandler := managev1connect.NewFileServiceHandler(filemedia.NewConnectHandler(fileService), r.manageOptions...)
	deps.mux.Handle(filePath, fileHandler)
	slog.Info("Registered service", "path", filePath)
	linkPreviewPath, linkPreviewHandler := managev1connect.NewLinkPreviewServiceHandler(
		filemedia.NewLinkPreviewConnectHandler(filemedia.NewLinkPreviewService(deps.spicedbClient)),
		r.manageOptions...,
	)
	deps.mux.Handle(linkPreviewPath, linkPreviewHandler)
	slog.Info("Registered service", "path", linkPreviewPath)
	internalFileIngestService := filemedia.NewInternalConnectHandler(fileService)
	internalFileIngestPath, internalFileIngestHandler := intrav1connect.NewInternalFileIngestServiceHandler(
		internalFileIngestService,
		r.internalOptions...,
	)
	deps.mux.Handle(internalFileIngestPath, r.internalTrust.collab(internalFileIngestHandler))
	slog.Info("Registered internal service", "path", internalFileIngestPath)
}

func (r serviceRegistration) registerUploadRoutes(fileService *filemedia.Service) {
	deps := r.dependencies
	// Authenticated multipart upload plane. Managed public-asset bytes are
	// relayed through the API; approved editor and large-media types use the
	// short-lived public S3 presigned control endpoints.
	deps.mux.Handle("/upload/media-artifact", auth.RequireGatewaySession(deps.db, filemedia.WithUploadIdleTimeout(http.HandlerFunc(fileService.HandleClientMediaArtifact))))
	deps.mux.Handle("/upload/source", auth.RequireGatewaySession(deps.db, filemedia.WithUploadIdleTimeout(http.HandlerFunc(fileService.HandleUploadSource))))
	deps.mux.Handle(
		"/upload/part",
		auth.RequireGatewaySession(deps.db, filemedia.WithUploadIdleTimeout(http.HandlerFunc(fileService.HandleUploadPart))),
	)
	deps.mux.Handle(
		"/upload/prefix",
		auth.RequireGatewaySession(deps.db, filemedia.WithUploadIdleTimeout(http.HandlerFunc(fileService.HandleVerifyUploadPrefix))),
	)
	deps.mux.Handle(
		"/upload/part/presign",
		auth.RequireGatewaySession(deps.db, http.HandlerFunc(fileService.HandlePresignUploadPart)),
	)
	deps.mux.Handle(
		"/upload/part/confirm",
		auth.RequireGatewaySession(deps.db, http.HandlerFunc(fileService.HandleConfirmUploadPart)),
	)
	slog.Info("Registered handlers", "paths", []string{"/upload/source", "/upload/part", "/upload/prefix", "/upload/part/presign", "/upload/part/confirm"})

}

func (r serviceRegistration) newPublicFiles(downloadTTL time.Duration) (*filepublic.Service, error) {
	deps := r.dependencies
	publicFileService, err := filepublic.NewService(filepublic.Deps{
		Units: filepublicpostgres.NewUnits(deps.db, filepublicpostgres.Ports{Segments: mediaassetadapter.NewSegmentConfigs()}), SpiceDB: deps.spicedbClient,
		CDNDomain: deps.cfg.CDNURL, MediaDomain: deps.cfg.MediaURL, MediaSecret: deps.cfg.MediaSigningSecret, DownloadTTL: downloadTTL,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize public file service: %w", err)
	}
	return publicFileService, nil
}
