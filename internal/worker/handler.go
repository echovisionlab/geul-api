package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/scheduler"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
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
	translationMaxRetries   = 3
	translationRetryDelay   = time.Second
	translationRetryBackoff = 2.0

	// scheduledCampaignBatch bounds the Campaign delivery runs one wake-up
	// dispatches and resumes.
	scheduledCampaignBatch = 25
)

// Handlers routes the scheduler's wake-ups and the queue messages to the
// domains that own them. It decides the queue contract (terminal or
// retryable), what a cleanup selects and in which order, and opens the
// transactions the principal-less domain ports run in; it owns no table.
type Handlers struct {
	units           mutation.UnitOfWork[Jobs]
	files           FileMedia
	danglingFiles   DanglingFileCleanup
	publicAssets    PublicAssetCleanup
	ogCleanup       OGCleanup
	faviconCleanup  FaviconCleanup
	uploads         UploadSessions
	uploadStorage   UploadStorage
	fileIngest      FileIngestPublisher
	shareLinks      ShareLinks
	authCodes       AuthCodeIssuances
	queueArchives   QueueArchives
	memberDeletions MemberDeletions
	legal           LegalPolicies
	identities      IdentityLister
	timedBans       TimedBans
	geoIP           GeoIPRefresher
	campaigns       CampaignDeliveries
	emailDelivery   EmailDelivery
	bulkEmail       BulkEmail
	metadataAI      MetadataAIJobProcessor
	translationJobs TranslationJobProcessor
	siteOrigin      string
}

// Deps are the worker's collaborators. MetadataAI and TranslationJobs may be
// nil: their queues are then not consumed and the metadata AI recovery is
// skipped. SiteOrigin may be empty.
type Deps struct {
	Units           mutation.UnitOfWork[Jobs] // workerpostgres.NewUnits
	FileMedia       FileMedia
	DanglingFiles   DanglingFileCleanup
	PublicAssets    PublicAssetCleanup
	OGCleanup       OGCleanup
	FaviconCleanup  FaviconCleanup
	Uploads         UploadSessions
	UploadStorage   UploadStorage
	FileIngest      FileIngestPublisher
	ShareLinks      ShareLinks
	AuthCodes       AuthCodeIssuances
	QueueArchives   QueueArchives
	MemberDeletions MemberDeletions
	Legal           LegalPolicies
	Identities      IdentityLister
	TimedBans       TimedBans
	GeoIP           GeoIPRefresher
	Campaigns       CampaignDeliveries
	EmailDelivery   EmailDelivery
	BulkEmail       BulkEmail
	MetadataAI      MetadataAIJobProcessor  // optional
	TranslationJobs TranslationJobProcessor // optional
	SiteOrigin      string                  // optional; the legal notice links
}

// NewHandlers returns the worker handlers, or an error naming each missing
// required collaborator.
func NewHandlers(deps Deps) (*Handlers, error) {
	var missing []string
	for _, required := range []struct {
		name    string
		present bool
	}{
		{"Units", deps.Units != nil},
		{"FileMedia", deps.FileMedia != nil},
		{"DanglingFiles", deps.DanglingFiles != nil},
		{"PublicAssets", deps.PublicAssets != nil},
		{"OGCleanup", deps.OGCleanup != nil},
		{"FaviconCleanup", deps.FaviconCleanup != nil},
		{"Uploads", deps.Uploads != nil},
		{"UploadStorage", deps.UploadStorage != nil},
		{"FileIngest", deps.FileIngest != nil},
		{"ShareLinks", deps.ShareLinks != nil},
		{"AuthCodes", deps.AuthCodes != nil},
		{"QueueArchives", deps.QueueArchives != nil},
		{"MemberDeletions", deps.MemberDeletions != nil},
		{"Legal", deps.Legal != nil},
		{"Identities", deps.Identities != nil},
		{"TimedBans", deps.TimedBans != nil},
		{"GeoIP", deps.GeoIP != nil},
		{"Campaigns", deps.Campaigns != nil},
		{"EmailDelivery", deps.EmailDelivery != nil},
		{"BulkEmail", deps.BulkEmail != nil},
	} {
		if !required.present {
			missing = append(missing, required.name)
		}
	}
	if len(missing) > 0 {
		return nil, errors.New("worker dependencies are required: " + strings.Join(missing, ", "))
	}
	return &Handlers{
		units:           deps.Units,
		files:           deps.FileMedia,
		danglingFiles:   deps.DanglingFiles,
		publicAssets:    deps.PublicAssets,
		ogCleanup:       deps.OGCleanup,
		faviconCleanup:  deps.FaviconCleanup,
		uploads:         deps.Uploads,
		uploadStorage:   deps.UploadStorage,
		fileIngest:      deps.FileIngest,
		shareLinks:      deps.ShareLinks,
		authCodes:       deps.AuthCodes,
		queueArchives:   deps.QueueArchives,
		memberDeletions: deps.MemberDeletions,
		legal:           deps.Legal,
		identities:      deps.Identities,
		timedBans:       deps.TimedBans,
		geoIP:           deps.GeoIP,
		campaigns:       deps.Campaigns,
		emailDelivery:   deps.EmailDelivery,
		bulkEmail:       deps.BulkEmail,
		metadataAI:      deps.MetadataAI,
		translationJobs: deps.TranslationJobs,
		siteOrigin:      deps.SiteOrigin,
	}, nil
}

// HandleScheduled processes one coalescible scheduler wake-up.
func (h *Handlers) HandleScheduled(ctx context.Context, job scheduler.Job) error {
	switch job {
	// Cleanup jobs
	case scheduler.JobCleanupDangling:
		return h.handleCleanupDanglingFiles(ctx, time.Now().UTC())
	case scheduler.JobCleanupPublicAssets:
		return h.handleCleanupPublicAssets(ctx, time.Now().UTC())
	case scheduler.JobCleanupIncomplete:
		return h.handleCleanupIncompleteUploads(ctx, time.Now())
	case scheduler.JobCleanupShareLinks:
		return h.handleCleanupShareLinks(ctx, time.Now())
	case scheduler.JobCleanupAuthCodeIssuance:
		return h.handleCleanupAuthCodeIssuance(ctx, time.Now())
	case scheduler.JobCleanupPGMQArchives:
		return h.handleCleanupPGMQArchives(ctx)
	case scheduler.JobProcessUserDeletions:
		return h.handleProcessUserDeletions(ctx, time.Now().UTC())

	// Policy jobs
	case scheduler.JobActivateTerms:
		return h.handleActivateLegal(ctx, termsActivation)
	case scheduler.JobActivatePrivacy:
		return h.handleActivateLegal(ctx, privacyActivation)
	case scheduler.JobUnbanExpired:
		return h.handleUnbanExpired(ctx, time.Now().UTC())

	// Maintenance jobs
	case scheduler.JobUpdateGeoIP:
		return h.handleUpdateGeoIP(ctx, time.Now().UTC())
	case scheduler.JobProcessScheduledCampaigns:
		if _, err := h.campaigns.DispatchDueEmailDeliveryRuns(ctx, scheduledCampaignBatch); err != nil {
			return err
		}
		_, err := h.campaigns.ResumeActiveEmailDeliveryRuns(ctx, scheduledCampaignBatch)
		return err
	case scheduler.JobProcessScheduledPosts:
		return h.units.Transaction(ctx, false, func(tx mutation.Tx[Jobs]) error {
			_, err := post.ProcessDueScheduledPosts(ctx, tx.Store().ScheduledPosts(), 0)
			return err
		})
	case scheduler.JobRecoverMetadataAI:
		return h.handleRecoverMetadataAI(ctx)

	default:
		return fmt.Errorf("unknown scheduler job: %q", job)
	}
}

func (h *Handlers) handleRecoverMetadataAI(ctx context.Context) error {
	if h.metadataAI == nil {
		return nil
	}
	republished, err := h.metadataAI.RecoverExpiredJobs(ctx, 0)
	if err != nil {
		return err
	}
	if republished > 0 {
		slog.Info("Expired metadata AI jobs republished", "count", republished)
	}
	return nil
}

// HandleEmailSend handles transactional email delivery events.
func (h *Handlers) HandleEmailSend(ctx context.Context, event *managev1.SendEmailEvent) error {
	_, err := h.handleEmailDelivery(ctx, event)
	return err
}
