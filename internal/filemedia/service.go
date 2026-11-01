package filemedia

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/favicon"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// Deps are the collaborators of the File use cases.
type Deps struct {
	IngestLocks    domain.IngestLocks
	FaviconCommits domain.FaviconCommits
	Units          mutation.UnitOfWork[Store]
	Fence          identitystate.Fence

	SpiceDB mutation.Authorizer
	Audit   domainaudit.Appender
	S3      *s3.Client
	// Presigner signs multipart part uploads; nil signs with S3.
	Presigner      *s3.PresignClient
	AsyncPublisher AsyncPublisher
	S3Bucket       string
	CDNDomain      string
	MediaDomain    string
	MediaSecret    string
	// DownloadTTL bounds signed download URLs; zero or more than
	// mediaauth.DownloadTTL signs for mediaauth.DownloadTTL.
	DownloadTTL time.Duration
	// EditorImageMaxSize overrides the editor image upload limit; zero keeps
	// the default.
	EditorImageMaxSize int64
	LegalRoutes        LegalRouteIdentity

	MemberSummaries MemberSummaries
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"IngestLocks": d.IngestLocks == nil, "FaviconCommits": d.FaviconCommits == nil, "Units": d.Units == nil, "Fence": d.Fence == nil, "SpiceDB": d.SpiceDB == nil, "Audit": d.Audit == nil, "S3": d.S3 == nil,
		"AsyncPublisher": d.AsyncPublisher == nil,
		"LegalRoutes":    d.LegalRoutes == nil, "MemberSummaries": d.MemberSummaries == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("file media dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the File use cases: the administrator File library, uploads
// and imports, media deliveries and download policies, and the File ports
// other domains call. Connect and MCP adapters translate their requests into
// its commands and queries.
type Service struct {
	ingestLocks     domain.IngestLocks
	faviconCommits  domain.FaviconCommits
	uploadReads     fileStoreReader
	ownerPreflights fileStoreReader
	units           mutation.UnitOfWork[Store]
	storeMutations  *mutation.StoreRunner[Store]

	s3Bucket        string
	cdnDomain       string
	mediaDomain     string
	mediaSecret     string
	downloadTTL     time.Duration
	s3Client        *s3.Client
	s3PresignClient *s3.PresignClient
	asyncPublisher  AsyncPublisher
	spiceDB         mutation.Authorizer
	auditWriter     domainaudit.Appender

	memberSummaries           MemberSummaries
	remoteImportResolver      remoteImportResolver
	remoteImportDialer        remoteImportDialer
	remoteImportBaseTransport *http.Transport
	legalRouteIdentity        LegalRouteIdentity

	uploadConfigs                      map[string]*domain.UploadConfig
	faviconProcessor                   favicon.Processor
	testFaviconBundleTimeout           time.Duration
	testFaviconCommitError             error
	testFaviconReconcileError          error
	testVerifiedIngestCommitError      error
	testBeforeTrackSessionInsert       func(domain.UploadSession)
	testAfterTrackSessionInsert        func(domain.UploadSession)
	testBeforeResumeReconciliation     func(domain.UploadSession)
	testBeforeFileManagerDeliveryFence func([]string)
	testAfterFileManagerDeliverySigned func([]string)
	testBeforeManageDeliveryFence      func([]string)
	testBeforeManagePrincipalLock      func([]string)
	testAfterManageDeliverySigned      func([]string)
	testAfterFileMutationPrincipal     func([]string)
	testAfterScopedFeaturedSigned      func(string, string, string)
	testAfterScopedBlockMediaSigned    func(string, string, []string)
}

// NewService validates deps and returns the File use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	storeMutations, err := mutation.NewStoreRunner(deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
	if err != nil {
		return nil, err
	}
	presigner := deps.Presigner
	if presigner == nil {
		presigner = s3.NewPresignClient(deps.S3)
	}
	downloadTTL := mediaauth.DownloadTTL
	if deps.DownloadTTL > 0 {
		downloadTTL = min(deps.DownloadTTL, mediaauth.DownloadTTL)
	}
	uploadConfigs := cloneUploadConfigs()
	if editorImage := uploadConfigs[managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE.String()]; editorImage != nil && deps.EditorImageMaxSize > 0 {
		editorImage.MaxSize = deps.EditorImageMaxSize
	}
	return &Service{
		ingestLocks:        deps.IngestLocks,
		faviconCommits:     deps.FaviconCommits,
		units:              deps.Units,
		storeMutations:     storeMutations,
		spiceDB:            deps.SpiceDB,
		auditWriter:        deps.Audit,
		s3Client:           deps.S3,
		s3PresignClient:    presigner,
		asyncPublisher:     deps.AsyncPublisher,
		s3Bucket:           deps.S3Bucket,
		cdnDomain:          deps.CDNDomain,
		mediaDomain:        deps.MediaDomain,
		mediaSecret:        deps.MediaSecret,
		downloadTTL:        downloadTTL,
		uploadConfigs:      uploadConfigs,
		faviconProcessor:   favicon.NewProcessor(),
		legalRouteIdentity: deps.LegalRoutes,
		memberSummaries:    deps.MemberSummaries,
	}, nil
}
