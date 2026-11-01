package filemedia

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/favicon"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

// Deps are the collaborators of the File use cases.
type Deps struct {
	DB      *gorm.DB
	SpiceDB *auth.SpiceDBClient
	Audit   domainaudit.Appender
	S3      *s3.Client
	// Presigner signs multipart part uploads; nil signs with S3.
	Presigner      *s3.PresignClient
	AsyncPublisher AsyncPublisher
	Transcoder     mq.TranscoderPublisher
	S3Bucket       string
	CDNDomain      string
	MediaDomain    string
	MediaSecret    string
	// DownloadTTL bounds signed download URLs; zero or more than
	// mediaauth.DownloadTTL signs for mediaauth.DownloadTTL.
	DownloadTTL time.Duration
	// EditorImageMaxSize overrides the editor image upload limit; zero keeps
	// the default.
	EditorImageMaxSize     int64
	LegalRoutes            LegalRouteIdentity
	PostAccess             PostAccess
	PagePolicyAccess       PagePolicyAccess
	WorkAttachment         WorkAttachment
	WorkPolicyAccess       WorkPolicyAccess
	ProgramEventAttachment ProgramEventAttachment
	TrackAttachment        TrackAttachment
	ReleasePolicyAccess    ReleasePolicyAccess
	AudienceAccess         AudienceAccess
	MemberSummaries        MemberSummaries
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "Audit": d.Audit == nil, "S3": d.S3 == nil,
		"AsyncPublisher": d.AsyncPublisher == nil, "Transcoder": d.Transcoder == nil,
		"LegalRoutes": d.LegalRoutes == nil, "PostAccess": d.PostAccess == nil,
		"PagePolicyAccess": d.PagePolicyAccess == nil, "WorkAttachment": d.WorkAttachment == nil,
		"WorkPolicyAccess": d.WorkPolicyAccess == nil, "ProgramEventAttachment": d.ProgramEventAttachment == nil,
		"TrackAttachment": d.TrackAttachment == nil, "ReleasePolicyAccess": d.ReleasePolicyAccess == nil,
		"AudienceAccess": d.AudienceAccess == nil, "MemberSummaries": d.MemberSummaries == nil,
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
	managev1connect.UnimplementedFileServiceHandler
	db                        *gorm.DB
	s3Bucket                  string
	cdnDomain                 string
	mediaDomain               string
	mediaSecret               string
	downloadTTL               time.Duration
	s3Client                  *s3.Client
	s3PresignClient           *s3.PresignClient
	asyncPublisher            AsyncPublisher
	publisher                 mq.TranscoderPublisher
	spiceDB                   *auth.SpiceDBClient
	auditWriter               domainaudit.Appender
	mutations                 *mutation.Runner
	postAccess                PostAccess
	pagePolicyAccess          PagePolicyAccess
	workAttachment            WorkAttachment
	workPolicyAccess          WorkPolicyAccess
	programEventAttachment    ProgramEventAttachment
	trackAttachment           TrackAttachment
	releasePolicyAccess       ReleasePolicyAccess
	audienceAccess            AudienceAccess
	memberSummaries           MemberSummaries
	remoteImportResolver      remoteImportResolver
	remoteImportDialer        remoteImportDialer
	remoteImportBaseTransport *http.Transport
	legalRouteIdentity        LegalRouteIdentity

	uploadConfigs                      map[managev1.UploadType]*model.UploadConfig
	faviconProcessor                   favicon.Processor
	testFaviconBundleTimeout           time.Duration
	testFaviconCommitError             error
	testFaviconReconcileError          error
	testVerifiedIngestCommitError      error
	testBeforeTrackSessionInsert       func(model.UploadSession)
	testAfterTrackSessionInsert        func(model.UploadSession)
	testBeforeResumeReconciliation     func(model.UploadSession)
	testBeforeFileManagerDeliveryFence func([]string)
	testAfterFileManagerDeliverySigned func([]string)
	testBeforeManageDeliveryFence      func([]string)
	testBeforeManagePrincipalLock      func([]string)
	testAfterManageDeliverySigned      func([]string)
	testBeforeFileMutationPrincipal    func(*gorm.DB, []string)
	testAfterFileMutationPrincipal     func([]string)
	testAfterScopedFeaturedSigned      func(string, string, string)
	testAfterScopedBlockMediaSigned    func(string, string, []string)
}

// NewService validates deps and returns the File use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewRunner(deps.DB, deps.SpiceDB, deps.Audit)
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
	if editorImage := uploadConfigs[managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE]; editorImage != nil && deps.EditorImageMaxSize > 0 {
		editorImage.MaxSize = deps.EditorImageMaxSize
	}
	return &Service{
		db: deps.DB, spiceDB: deps.SpiceDB, auditWriter: deps.Audit, mutations: mutations,
		s3Client: deps.S3, s3PresignClient: presigner, asyncPublisher: deps.AsyncPublisher,
		publisher: deps.Transcoder, s3Bucket: deps.S3Bucket, cdnDomain: deps.CDNDomain,
		mediaDomain: deps.MediaDomain, mediaSecret: deps.MediaSecret, downloadTTL: downloadTTL,
		uploadConfigs: uploadConfigs, faviconProcessor: favicon.NewProcessor(),
		legalRouteIdentity: deps.LegalRoutes, postAccess: deps.PostAccess,
		pagePolicyAccess: deps.PagePolicyAccess, workAttachment: deps.WorkAttachment,
		workPolicyAccess: deps.WorkPolicyAccess, programEventAttachment: deps.ProgramEventAttachment,
		trackAttachment: deps.TrackAttachment, releasePolicyAccess: deps.ReleasePolicyAccess,
		audienceAccess: deps.AudienceAccess, memberSummaries: deps.MemberSummaries,
	}, nil
}
