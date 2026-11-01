package workerpostgres

import (
	"errors"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"gorm.io/gorm"

	accountpostgres "github.com/echovisionlab/geul-api/internal/account/postgres"
	campaignadapter "github.com/echovisionlab/geul-api/internal/adapters/campaign"
	emaildeliveryadapter "github.com/echovisionlab/geul-api/internal/adapters/emaildelivery"
	geoipadapter "github.com/echovisionlab/geul-api/internal/adapters/geoip"
	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/config"
	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/geoip"
	"github.com/echovisionlab/geul-api/internal/mq"
	ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"
	"github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/worker"
)

// MetadataAIJobs runs and recovers the metadata AI jobs
// (*ai.MetadataJobManager).
type MetadataAIJobs interface {
	worker.MetadataAIJobProcessor
	MetadataAIRecoverer
}

// Config holds the worker's infrastructure and the runtimes of the domains
// it drives. MetadataAI, TranslationJobs and CampaignEmail may be nil.
type Config struct {
	DB              *gorm.DB
	Config          *config.Config
	Publisher       *mq.Publisher
	AdapterLoader   emaildeliveryadapter.MailAdapterLoader
	S3Client        *s3.Client
	KratosClient    auth.IdentityManager
	SpiceDB         *auth.SpiceDBClient
	Audit           *telemetry.DurableWriter
	OGPlanner       *ogpostgres.Planner
	MemberDeletion  accountpostgres.MemberDeletion
	FileMedia       worker.FileMedia
	MediaCleanup    worker.DanglingFileCleanup
	PublicAssets    worker.PublicAssetCleanup
	OGCleanup       worker.OGCleanup
	FaviconCleanup  worker.FaviconCleanup
	MetadataAI      MetadataAIJobs                             // optional
	TranslationJobs worker.TranslationJobProcessor             // optional
	CampaignEmail   emaildeliveryadapter.CampaignEmailRenderer // optional: live Campaign renders then fail
}

func (c Config) validate() error {
	var missing []string
	for _, required := range []struct {
		name    string
		present bool
	}{
		{"DB", c.DB != nil},
		{"Config", c.Config != nil},
		{"Publisher", c.Publisher != nil},
		{"AdapterLoader", c.AdapterLoader != nil},
		{"S3Client", c.S3Client != nil},
		{"KratosClient", c.KratosClient != nil},
		{"SpiceDB", c.SpiceDB != nil},
		{"Audit", c.Audit != nil},
		{"OGPlanner", c.OGPlanner != nil},
		{"MemberDeletion", c.MemberDeletion != nil},
	} {
		if !required.present {
			missing = append(missing, required.name)
		}
	}
	if len(missing) > 0 {
		return errors.New("worker infrastructure is required: " + strings.Join(missing, ", "))
	}
	return nil
}

// New binds every worker port to its production adapter.
func New(c Config) (worker.Deps, error) {
	if err := c.validate(); err != nil {
		return worker.Deps{}, err
	}
	ports := Ports{Audit: c.Audit, OGPlanner: c.OGPlanner}
	deps := worker.Deps{
		FileMedia:      c.FileMedia,
		DanglingFiles:  c.MediaCleanup,
		PublicAssets:   c.PublicAssets,
		OGCleanup:      c.OGCleanup,
		FaviconCleanup: c.FaviconCleanup,
		SiteOrigin:     c.Config.SiteOrigin,
	}
	if c.MetadataAI != nil {
		ports.MetadataAI = c.MetadataAI
		deps.MetadataAI = c.MetadataAI
	}
	if c.TranslationJobs != nil {
		deps.TranslationJobs = c.TranslationJobs
	}
	units, err := NewUnits(c.DB, ports)
	if err != nil {
		return worker.Deps{}, err
	}
	deps.Units = units

	campaigns := campaignadapter.NewDeliveryDispatcher(c.DB, campaign.NewAuditedCampaignDeliveryDispatcher(
		c.SpiceDB,
		c.Publisher,
		c.Audit,
		campaign.WithLegalNoticeDeliveryPort(legaladapter.NewRuntime()),
	))
	metrics := emaildeliveryadapter.NewMetrics()
	suppressions := emaildeliveryadapter.NewSuppressionStore(c.DB)

	deps.Uploads = uploadSessions{db: c.DB}
	deps.UploadStorage = uploadStorage{client: c.S3Client, bucket: c.Config.S3Bucket}
	deps.FileIngest = c.Publisher
	deps.ShareLinks = shareLinks{db: c.DB}
	deps.AuthCodes = authCodeIssuances{db: c.DB}
	deps.QueueArchives = queueArchives{db: c.DB}
	deps.MemberDeletions = memberDeletions{
		db: c.DB, publisher: c.Publisher, identity: c.KratosClient, spiceDB: c.SpiceDB,
		lifecycle: c.MemberDeletion, audit: c.Audit,
	}
	deps.Legal = legalPolicies{db: c.DB, notices: campaigns}
	deps.Identities = c.KratosClient
	deps.TimedBans = timedBans{db: c.DB, identities: c.KratosClient, audit: c.Audit}
	deps.GeoIP = geoip.NewRefreshService(geoipadapter.NewRefreshExecutor(
		c.DB, c.Config.MaxMindAccountID, c.Config.MaxMindLicenseKey,
	))
	deps.Campaigns = campaigns
	deps.EmailDelivery = emaildelivery.NewDeliveryApplication(
		emaildeliveryadapter.NewCampaignDeliveryStore(c.DB, c.Audit, metrics),
		emaildeliveryadapter.NewRecipientPolicy(c.DB, c.KratosClient),
		emaildeliveryadapter.NewRenderer(c.DB, c.Config.CDNURL, c.Config.SiteOrigin, c.CampaignEmail),
		suppressions,
		emaildeliveryadapter.NewProviderLoader(c.AdapterLoader),
		metrics,
	)
	deps.BulkEmail = emaildelivery.NewBulkApplication(
		emaildeliveryadapter.NewBulkCampaignStore(
			c.DB, c.SpiceDB, c.Audit, metrics, c.Config.TokenSigningSecret, c.Config.SiteOrigin,
		),
		suppressions,
		c.Publisher,
		metrics,
	)
	return deps, nil
}
