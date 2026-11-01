package worker

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"google.golang.org/protobuf/proto"

	"github.com/echovisionlab/geul-api/internal/account"
	campaignadapter "github.com/echovisionlab/geul-api/internal/adapters/campaign"
	filemediaruntime "github.com/echovisionlab/geul-api/internal/adapters/filemedia/runtime"
	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/config"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/favicon"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/scheduler"
	"github.com/echovisionlab/geul-api/internal/telemetry"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

const (
	// Transactional and campaign mail retries for about 85 minutes (5 s
	// doubling, ten times) so a database or mail adapter outage does not
	// exhaust the budget before it recovers.
	emailSendMaxRetries     = 10
	emailSendRetryDelay     = 5 * time.Second
	emailSendRetryBackoff   = 2.0
	authEmailMaxRetries     = 4
	authEmailRetryDelay     = 30 * time.Second
	mediaResultMaxRetries   = 2
	mediaResultRetryDelay   = 500 * time.Millisecond
	userDeletionMaxRetries  = 5
	userDeletionRetryDelay  = 5 * time.Second
	schedulerMaxRetries     = 3
	schedulerRetryDelay     = time.Second
	schedulerRetryBackoff   = 2.0
	translationMaxRetries   = 3
	translationRetryDelay   = time.Second
	translationRetryBackoff = 2.0
)

type Handlers struct {
	db               *gorm.DB
	config           *config.Config
	s3Client         *s3.Client
	publisher        workerPublisher
	fileIngest       fileIngestPublisher
	adapterLoader    mailAdapterLoader
	kratosClient     auth.IdentityManager
	spicedbClient    *auth.SpiceDBClient
	fileMediaRuntime *filemediaruntime.Runtime
	mediaCleanup     *mediaasset.Cleanup
	publicAssets     *mediaasset.PublicAssetCleanup
	ogCleanup        *og.Cleanup
	faviconCleanup   *favicon.Cleanup
	metadataAI       MetadataAIJobProcessor
	translationJobs  TranslationJobProcessor
	mailMetrics      mailMetrics
	httpClient       httpDoer
	auditWriter      *telemetry.DurableWriter
	ogPlanner        *og.Planner
	memberDeletion   account.MemberDeletionLifecycle
	campaignEmail    CampaignEmailRenderer
}

type CampaignEmailRenderer interface {
	RenderCampaignEmail(
		context.Context,
		*gorm.DB,
		string,
		string,
		map[string]string,
	) (*email.RenderedEmail, error)
}

type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

type MetadataAIJobProcessor interface {
	ProcessJob(ctx context.Context, jobID string) error
	RecoverExpiredJobs(ctx context.Context, limit int) (int, error)
}

type TranslationJobProcessor interface {
	ProcessDelivery(ctx context.Context, jobID string) error
}

type mailAdapterLoader interface {
	GetActiveAdapters(ctx context.Context) ([]email.Adapter, error)
}

type fileIngestPublisher interface {
	PublishFileIngest(ctx context.Context, event proto.Message) error
}

type workerPublisher interface {
	campaign.CampaignBulkPublisher
	PublishFileDelete(ctx context.Context, event *managev1.FileDeleteEvent) error
	PublishMediaProcessingLifecycle(ctx context.Context, event *managev1.MediaProcessingLifecycleEvent) error
	PublishSendEmail(ctx context.Context, event *managev1.SendEmailEvent) error
	PublishTranscodeCancel(ctx context.Context, event *managev1.TranscodeCancelEvent) error
	PublishWaveformGenerate(ctx context.Context, event *managev1.WaveformGenerateEvent) error
}

// HandlerDeps are the worker's collaborators. MetadataAI, TranslationJobs
// and CampaignEmail may be nil; NewHandlers panics on any other missing one.
type HandlerDeps struct {
	DB               *gorm.DB
	Config           *config.Config
	Publisher        *mq.Publisher
	AdapterLoader    mailAdapterLoader
	S3Client         *s3.Client
	KratosClient     auth.IdentityManager
	FileMediaRuntime *filemediaruntime.Runtime
	MediaCleanup     *mediaasset.Cleanup
	PublicAssets     *mediaasset.PublicAssetCleanup
	OGCleanup        *og.Cleanup
	FaviconCleanup   *favicon.Cleanup
	MetadataAI       MetadataAIJobProcessor
	TranslationJobs  TranslationJobProcessor
	AuditWriter      *telemetry.DurableWriter
	SpiceDB          *auth.SpiceDBClient
	OGPlanner        *og.Planner
	MemberDeletion   account.MemberDeletionLifecycle
	CampaignEmail    CampaignEmailRenderer
}

func NewHandlers(deps HandlerDeps) *Handlers {
	if deps.DB == nil {
		panic("worker.NewHandlers: db is required")
	}
	if deps.Config == nil {
		panic("worker.NewHandlers: config is required")
	}
	if deps.Publisher == nil {
		panic("worker.NewHandlers: publisher is required")
	}
	if deps.AdapterLoader == nil {
		panic("worker.NewHandlers: adapterLoader is required")
	}
	if deps.S3Client == nil {
		panic("worker.NewHandlers: s3Client is required")
	}
	if deps.KratosClient == nil {
		panic("worker.NewHandlers: kratosClient is required")
	}
	if deps.FileMediaRuntime == nil {
		panic("worker.NewHandlers: FileMedia runtime is required")
	}
	if deps.MediaCleanup == nil || deps.PublicAssets == nil || deps.OGCleanup == nil || deps.FaviconCleanup == nil {
		panic("worker.NewHandlers: MediaAsset, OG, and Favicon cleanup applications are required")
	}
	if deps.AuditWriter == nil {
		panic("worker.NewHandlers: auditWriter is required")
	}
	if deps.SpiceDB == nil {
		panic("worker.NewHandlers: SpiceDB client is required")
	}
	if deps.OGPlanner == nil {
		panic("worker.NewHandlers: OG planner is required")
	}
	if deps.MemberDeletion == nil {
		panic("worker.NewHandlers: Member deletion lifecycle is required")
	}
	return &Handlers{
		db:               deps.DB,
		config:           deps.Config,
		publisher:        deps.Publisher,
		fileIngest:       deps.Publisher,
		adapterLoader:    deps.AdapterLoader,
		s3Client:         deps.S3Client,
		kratosClient:     deps.KratosClient,
		spicedbClient:    deps.SpiceDB,
		fileMediaRuntime: deps.FileMediaRuntime,
		mediaCleanup:     deps.MediaCleanup,
		publicAssets:     deps.PublicAssets,
		ogCleanup:        deps.OGCleanup,
		faviconCleanup:   deps.FaviconCleanup,
		metadataAI:       deps.MetadataAI,
		translationJobs:  deps.TranslationJobs,
		mailMetrics:      newMailMetrics(),
		httpClient:       http.DefaultClient,
		auditWriter:      deps.AuditWriter,
		ogPlanner:        deps.OGPlanner,
		memberDeletion:   deps.MemberDeletion,
		campaignEmail:    deps.CampaignEmail,
	}
}

// HandleScheduled processes one coalescible scheduler wake-up.
func (h *Handlers) HandleScheduled(ctx context.Context, job scheduler.Job) error {
	switch job {
	// Cleanup jobs
	case scheduler.JobCleanupDangling:
		return h.handleCleanupDanglingFiles(ctx)
	case scheduler.JobCleanupPublicAssets:
		return h.handleCleanupPublicAssets(ctx, time.Now().UTC())
	case scheduler.JobCleanupIncomplete:
		return h.handleCleanupIncompleteUploads(ctx)
	case scheduler.JobCleanupShareLinks:
		return h.handleCleanupShareLinks(ctx)
	case scheduler.JobCleanupAuthCodeIssuance:
		return h.handleCleanupAuthCodeIssuance(ctx)
	case scheduler.JobCleanupPGMQArchives:
		return h.handleCleanupPGMQArchives(ctx)
	case scheduler.JobProcessUserDeletions:
		return h.handleProcessUserDeletions(ctx)

	// Policy jobs
	case scheduler.JobActivateTerms:
		return h.handleActivateTerms(ctx)
	case scheduler.JobActivatePrivacy:
		return h.handleActivatePrivacy(ctx)
	case scheduler.JobUnbanExpired:
		return h.handleUnbanExpired(ctx)

	// Maintenance jobs
	case scheduler.JobUpdateGeoIP:
		return h.handleUpdateGeoIP(ctx)
	case scheduler.JobProcessScheduledCampaigns:
		dispatcher := h.newCampaignDeliveryDispatcher()
		if _, err := dispatcher.DispatchDueEmailDeliveryRuns(ctx, 25); err != nil {
			return err
		}
		_, err := dispatcher.ResumeActiveEmailDeliveryRuns(ctx, 25)
		return err
	case scheduler.JobProcessScheduledPosts:
		_, err := post.ProcessDueScheduledPosts(ctx, h.db, 0)
		return err
	case scheduler.JobRecoverMetadataAI:
		if h.metadataAI == nil {
			return nil
		}
		_, err := h.metadataAI.RecoverExpiredJobs(ctx, 0)
		return err

	default:
		return fmt.Errorf("unknown scheduler job: %q", job)
	}
}

func (h *Handlers) newCampaignDeliveryDispatcher() *campaignadapter.DeliveryDispatcher {
	return campaignadapter.NewDeliveryDispatcher(h.db, campaign.NewAuditedCampaignDeliveryDispatcher(
		h.spicedbClient,
		h.publisher,
		h.auditWriter,
		campaign.WithLegalNoticeDeliveryPort(legaladapter.NewRuntime()),
	))
}

// HandleEmailSend handles transactional email delivery events.
func (h *Handlers) HandleEmailSend(ctx context.Context, event *managev1.SendEmailEvent) error {
	return h.handleSendEmail(ctx, event)
}
