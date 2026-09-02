package worker

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"google.golang.org/protobuf/proto"

	"github.com/echovisionlab/geul-api/internal/account"
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
	emailSendMaxRetries     = 5
	emailSendRetryDelay     = time.Second
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

type HandlerOption func(*Handlers)

func WithCampaignEmailRenderer(renderer CampaignEmailRenderer) HandlerOption {
	return func(handlers *Handlers) {
		handlers.campaignEmail = renderer
	}
}

type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

type MetadataAIJobProcessor interface {
	ProcessJob(ctx context.Context, jobID string) error
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

func NewHandlers(
	db *gorm.DB,
	cfg *config.Config,
	publisher *mq.Publisher,
	adapterLoader mailAdapterLoader,
	s3Client *s3.Client,
	kratosClient auth.IdentityManager,
	fileMediaRuntime *filemediaruntime.Runtime,
	mediaCleanup *mediaasset.Cleanup,
	publicAssets *mediaasset.PublicAssetCleanup,
	ogCleanup *og.Cleanup,
	faviconCleanup *favicon.Cleanup,
	metadataAI MetadataAIJobProcessor,
	translationJobs TranslationJobProcessor,
	auditWriter *telemetry.DurableWriter,
	spicedbClient *auth.SpiceDBClient,
	ogPlanner *og.Planner,
	memberDeletion account.MemberDeletionLifecycle,
	options ...HandlerOption,
) *Handlers {
	if db == nil {
		panic("worker.NewHandlers: db is required")
	}
	if cfg == nil {
		panic("worker.NewHandlers: config is required")
	}
	if publisher == nil {
		panic("worker.NewHandlers: publisher is required")
	}
	if adapterLoader == nil {
		panic("worker.NewHandlers: adapterLoader is required")
	}
	if s3Client == nil {
		panic("worker.NewHandlers: s3Client is required")
	}
	if kratosClient == nil {
		panic("worker.NewHandlers: kratosClient is required")
	}
	if fileMediaRuntime == nil {
		panic("worker.NewHandlers: FileMedia runtime is required")
	}
	if mediaCleanup == nil || publicAssets == nil || ogCleanup == nil || faviconCleanup == nil {
		panic("worker.NewHandlers: MediaAsset, OG, and Favicon cleanup applications are required")
	}
	if auditWriter == nil {
		panic("worker.NewHandlers: auditWriter is required")
	}
	if spicedbClient == nil {
		panic("worker.NewHandlers: SpiceDB client is required")
	}
	if ogPlanner == nil {
		panic("worker.NewHandlers: OG planner is required")
	}
	if memberDeletion == nil {
		panic("worker.NewHandlers: Member deletion lifecycle is required")
	}
	handlers := &Handlers{
		db:               db,
		config:           cfg,
		publisher:        publisher,
		fileIngest:       publisher,
		adapterLoader:    adapterLoader,
		s3Client:         s3Client,
		kratosClient:     kratosClient,
		spicedbClient:    spicedbClient,
		fileMediaRuntime: fileMediaRuntime,
		mediaCleanup:     mediaCleanup,
		publicAssets:     publicAssets,
		ogCleanup:        ogCleanup,
		faviconCleanup:   faviconCleanup,
		metadataAI:       metadataAI,
		translationJobs:  translationJobs,
		mailMetrics:      newMailMetrics(),
		httpClient:       http.DefaultClient,
		auditWriter:      auditWriter,
		ogPlanner:        ogPlanner,
		memberDeletion:   memberDeletion,
	}
	for _, option := range options {
		option(handlers)
	}
	return handlers
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

	default:
		return fmt.Errorf("unknown scheduler job: %q", job)
	}
}

func (h *Handlers) newCampaignDeliveryDispatcher() *campaign.CampaignDeliveryDispatcher {
	return campaign.NewAuditedCampaignDeliveryDispatcher(
		h.db,
		h.spicedbClient,
		h.publisher,
		h.auditWriter,
		campaign.WithLegalNoticeDeliveryPort(legaladapter.NewRuntime()),
	)
}

// HandleEmailSend handles transactional email delivery events.
func (h *Handlers) HandleEmailSend(ctx context.Context, event *managev1.SendEmailEvent) error {
	return h.handleSendEmail(ctx, event)
}
