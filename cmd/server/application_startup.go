package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/adapters/filemediareuse"

	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"

	faviconpostgres "github.com/echovisionlab/geul-api/internal/favicon/postgres"
	memberpostgres "github.com/echovisionlab/geul-api/internal/member/postgres"
	transcodepostgres "github.com/echovisionlab/geul-api/internal/transcode/postgres"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"gorm.io/gorm"

	accountadapter "github.com/echovisionlab/geul-api/internal/adapters/account"
	campaignadapter "github.com/echovisionlab/geul-api/internal/adapters/campaign"
	emailauthoringadapter "github.com/echovisionlab/geul-api/internal/adapters/emailauthoring"
	filemediaruntime "github.com/echovisionlab/geul-api/internal/adapters/filemedia/runtime"
	mediaassetadapter "github.com/echovisionlab/geul-api/internal/adapters/mediaasset"
	"github.com/echovisionlab/geul-api/internal/adapters/ogprojection"
	translationadapter "github.com/echovisionlab/geul-api/internal/adapters/translation"
	"github.com/echovisionlab/geul-api/internal/ai"
	aipostgres "github.com/echovisionlab/geul-api/internal/ai/postgres"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authentication"
	authenticationpostgres "github.com/echovisionlab/geul-api/internal/authentication/postgres"
	"github.com/echovisionlab/geul-api/internal/config"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/email"
	emaildeliverypostgres "github.com/echovisionlab/geul-api/internal/emaildelivery/postgres"
	"github.com/echovisionlab/geul-api/internal/favicon"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/og"
	ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"
	"github.com/echovisionlab/geul-api/internal/telemetry"
	translationapplication "github.com/echovisionlab/geul-api/internal/translation/application"
	translationpostgres "github.com/echovisionlab/geul-api/internal/translation/postgres"
	"github.com/echovisionlab/geul-api/internal/worker"
	workerpostgres "github.com/echovisionlab/geul-api/internal/worker/postgres"
)

type applicationDependencies struct {
	db                      *gorm.DB
	sqlDB                   *sql.DB
	databaseDSN             string
	kratosClient            *auth.KratosClient
	spicedbClient           *auth.SpiceDBClient
	hooksPublisher          *mq.Publisher
	passwordHasher          *crypto.PasswordHasher
	ogPublisher             *mq.Publisher
	servicePublisher        *mq.Publisher
	authCodeIssuanceLimiter *authentication.AuthCodeIssuanceLimiter
	s3Client                *s3.Client
	s3PresignClient         *s3.PresignClient
	workerPublisher         *mq.Publisher
	transcodeTracker        *transcodepostgres.Tracker
	adapterLoader           *email.AdapterLoader
	metadataAIJobs          *ai.MetadataJobManager
	contentBlockStore       *contentblock.Store
	og                      *ogDependencies
	translationJobs         *translationapplication.TranslationJobManager
	telemetryWriter         *telemetry.DurableWriter
	workerHandlers          *worker.Handlers
}

func configureBootstrapLogging() slog.Handler {
	level := slog.LevelInfo
	if strings.EqualFold(os.Getenv("LOG_LEVEL"), "debug") {
		level = slog.LevelDebug
	}
	stdout := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(telemetry.NewNormalizingHandler(stdout)))
	return stdout
}

func initializeApplicationDependencies(
	ctx context.Context,
	cfg *config.Config,
) (result *applicationDependencies, resultErr error) {
	deps := &applicationDependencies{}
	defer func() {
		if resultErr != nil {
			deps.Close()
		}
	}()

	var err error
	deps.db, deps.sqlDB, err = connectDatabase(
		ctx,
		cfg.DatabaseDSN,
		cfg.DatabaseMaxOpen,
		cfg.DatabaseMaxIdle,
	)
	if err != nil {
		return nil, fmt.Errorf("initialize database: %w", err)
	}
	deps.databaseDSN = cfg.DatabaseDSN
	deps.telemetryWriter = telemetry.NewDurableWriter(deps.db)
	deps.spicedbClient, err = auth.NewSpiceDBClient(
		cfg.SpiceDBEndpoint,
		cfg.SpiceDBToken,
		cfg.SpiceDBAllowInsecure,
	)
	if err != nil {
		return nil, fmt.Errorf("initialize SpiceDB: %w", err)
	}
	deps.kratosClient = auth.NewKratosClient(cfg.KratosAdminURL)
	deps.hooksPublisher, err = mq.NewPublisher(deps.sqlDB)
	if err != nil {
		return nil, fmt.Errorf("initialize PGMQ publisher: %w", err)
	}
	deps.passwordHasher = newApplicationPasswordHasher(cfg)
	deps.ogPublisher = deps.hooksPublisher
	deps.servicePublisher = deps.hooksPublisher
	deps.authCodeIssuanceLimiter = newAuthCodeIssuanceLimiter(cfg, deps.db)
	deps.s3Client, err = newApplicationS3Client(ctx, cfg)
	if err != nil {
		return nil, err
	}
	publicS3Client, err := newApplicationS3ClientForEndpoint(ctx, cfg, cfg.S3PublicEndpoint)
	if err != nil {
		return nil, err
	}
	deps.s3PresignClient = s3.NewPresignClient(publicS3Client)
	deps.workerPublisher = deps.hooksPublisher
	deps.transcodeTracker = transcodepostgres.NewTracker(deps.db, deps.workerPublisher)
	deps.adapterLoader = email.NewAdapterLoader(emaildeliverypostgres.NewDeliveryReadStore(deps.db), nil)
	metadataAIUnits, err := aipostgres.NewUnits(deps.db, deps.servicePublisher)
	if err != nil {
		return nil, fmt.Errorf("initialize metadata AI store: %w", err)
	}
	metadataAIProvider, err := ai.NewGeminiProvider(cfg.GoogleAIAPIKey)
	if err != nil {
		return nil, fmt.Errorf("initialize metadata AI job manager: %w", err)
	}
	deps.metadataAIJobs, err = ai.NewMetadataJobManager(ai.JobManagerDeps{
		Units: metadataAIUnits, SpiceDB: deps.spicedbClient, Fence: identitystate.PostgresFence{},
		Audit: deps.telemetryWriter, Provider: metadataAIProvider,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize metadata AI job manager: %w", err)
	}
	deps.contentBlockStore, err = contentblock.NewGeneratedStore(
		filemediareuse.New(deps.spicedbClient),
	)
	if err != nil {
		return nil, fmt.Errorf("initialize Content Block store: %w", err)
	}
	deps.og, err = newOGDependencies(deps.db, cfg.CDNURL)
	if err != nil {
		return nil, fmt.Errorf("initialize OG dependencies: %w", err)
	}
	translationRegistry, err := translationadapter.NewDomainRegistry(
		emailauthoringadapter.NewCampaignDeliveryReferences(),
		deps.telemetryWriter,
	)
	if err != nil {
		return nil, fmt.Errorf("initialize translation domain registry: %w", err)
	}
	translationJobUnits, err := translationpostgres.NewJobUnits(deps.db, translationStorePorts(
		translationRegistry, deps.contentBlockStore, deps.og, deps.spicedbClient, deps.workerPublisher, cfg.CDNURL,
	))
	if err != nil {
		return nil, fmt.Errorf("initialize translation job store: %w", err)
	}
	translationLifecycle, err := translationadapter.NewLifecyclePublisher(deps.workerPublisher)
	if err != nil {
		return nil, fmt.Errorf("initialize translation lifecycle publisher: %w", err)
	}
	deps.translationJobs, err = translationapplication.NewTranslationJobManager(translationapplication.JobManagerDeps{
		Units: translationJobUnits, SpiceDB: deps.spicedbClient, Fence: identitystate.PostgresFence{},
		Audit: deps.telemetryWriter, Plans: translationRegistry, Lifecycle: translationLifecycle,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize translation job manager: %w", err)
	}
	fileMediaRuntime := filemediaruntime.New(
		deps.db,
		deps.workerPublisher,
		deps.transcodeTracker,
		deps.spicedbClient,
		deps.s3Client,
		cfg.S3Bucket,
		filemediaruntime.NewAuthorizationDeletion(deps.spicedbClient, nil),
	)
	cleanupStorage := mediaassetadapter.NewCleanupStorage(deps.s3Client, cfg.S3Bucket)
	ogCleanup := og.NewCleanup(deps.og.units)
	mediaCleanup := mediaasset.NewCleanup(mediaassetpostgres.NewCleanupUnits(deps.db), cleanupStorage, mediaassetadapter.NewActiveTranscodes(deps.db))
	publicAssetCache := mediaassetadapter.NewPublicAssetCache(
		cfg.CDNURL,
		cfg.CloudflareAPIURL,
		cfg.CloudflareZoneID,
		cfg.CloudflareAPIToken,
		cfg.CloudflareCachePurgeEnabled,
		http.DefaultClient,
	)
	publicAssetCleanup := mediaasset.NewPublicAssetCleanup(
		mediaassetpostgres.NewCleanupUnits(deps.db),
		cleanupStorage,
		publicAssetCache,
		ogpostgres.NewAssetProtector(deps.db, ogprojection.AssetReferences()...),
	)
	faviconCleanup := favicon.NewCleanup(faviconpostgres.NewUnits(deps.db, ""))
	workerDeps, err := workerpostgres.New(workerpostgres.Config{
		DB:              deps.db,
		Config:          cfg,
		Publisher:       deps.workerPublisher,
		AdapterLoader:   deps.adapterLoader,
		S3Client:        deps.s3Client,
		KratosClient:    deps.kratosClient,
		SpiceDB:         deps.spicedbClient,
		Audit:           deps.telemetryWriter,
		OGPlanner:       deps.og.planner,
		MemberDeletion:  accountadapter.MemberDeletion{},
		FileMedia:       fileMediaRuntime,
		MediaCleanup:    mediaCleanup,
		PublicAssets:    publicAssetCleanup,
		OGCleanup:       ogCleanup,
		FaviconCleanup:  faviconCleanup,
		MetadataAI:      deps.metadataAIJobs,
		TranslationJobs: deps.translationJobs,
		CampaignEmail:   campaignadapter.NewLiveEmailRenderer(deps.contentBlockStore),
	})
	if err != nil {
		return nil, fmt.Errorf("initialize worker: %w", err)
	}
	deps.workerHandlers, err = worker.NewHandlers(workerDeps)
	if err != nil {
		return nil, fmt.Errorf("initialize worker: %w", err)
	}
	logApplicationDependenciesReady(cfg)
	return deps, nil
}

func newApplicationPasswordHasher(cfg *config.Config) *crypto.PasswordHasher {
	return crypto.NewPasswordHasher(&crypto.Argon2idParams{
		Memory:      cfg.Argon2Memory,
		Iterations:  cfg.Argon2Iterations,
		Parallelism: cfg.Argon2Parallelism,
		SaltLength:  cfg.Argon2SaltLength,
		KeyLength:   cfg.Argon2KeyLength,
	})
}

func newAuthCodeIssuanceLimiter(
	cfg *config.Config,
	db *gorm.DB,
) *authentication.AuthCodeIssuanceLimiter {
	return authentication.NewAuthCodeIssuanceLimiter(
		authenticationpostgres.NewUnits(db),
		[]byte(cfg.TokenSigningSecret),
		authentication.AuthCodeIssuanceLimits{
			Cooldown:        time.Duration(cfg.AuthCodeResendCooldownSeconds) * time.Second,
			AddressHourly:   cfg.AuthCodeAddressHourlyLimit,
			IPTenMinute:     cfg.AuthCodeIPTenMinuteLimit,
			GlobalPerMinute: cfg.AuthCodeGlobalMinuteLimit,
		},
	)
}

func newApplicationS3Client(ctx context.Context, cfg *config.Config) (*s3.Client, error) {
	return newApplicationS3ClientForEndpoint(ctx, cfg, cfg.S3Endpoint)
}

func newApplicationS3ClientForEndpoint(ctx context.Context, cfg *config.Config, endpoint string) (*s3.Client, error) {
	awsConfig, err := awsconfig.LoadDefaultConfig(
		ctx,
		awsconfig.WithRegion(cfg.S3Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			cfg.S3AccessKeyID,
			cfg.S3SecretAccessKey,
			"",
		)),
		awsconfig.WithRequestChecksumCalculation(aws.RequestChecksumCalculationWhenRequired),
	)
	if err != nil {
		return nil, fmt.Errorf("load AWS config for S3: %w", err)
	}
	return s3.NewFromConfig(awsConfig, func(options *s3.Options) {
		if endpoint != "" {
			options.BaseEndpoint = aws.String(endpoint)
		}
		options.UsePathStyle = cfg.S3ForcePathStyle
	}), nil
}

func registerAuthenticationRoutes(
	mux *http.ServeMux,
	cfg *config.Config,
	deps *applicationDependencies,
) error {
	proxy, err := authentication.NewKratosPublicProxy(
		cfg.KratosPublicURL,
		deps.authCodeIssuanceLimiter,
		[]byte(cfg.TokenSigningSecret),
	)
	if err != nil {
		return fmt.Errorf("initialize Kratos public proxy: %w", err)
	}
	authenticationAccess, err := authentication.NewAuthenticationAccessRecorder(
		cfg.KratosPublicURL,
		authenticationpostgres.NewSessions(deps.db),
		deps.telemetryWriter,
	)
	if err != nil {
		return fmt.Errorf("initialize authentication access recorder: %w", err)
	}
	for _, path := range []string{"/self-service/", "/sessions/", "/schemas/", "/health/"} {
		mux.Handle(path, authenticationAccess.Wrap(proxy))
	}
	mux.Handle("/.well-known/ory/webauthn.js", authenticationAccess.Wrap(proxy))
	unifiedAuth, err := authentication.NewUnifiedAuthHandler(
		proxy,
		deps.kratosClient,
		authentication.RegistrationReuseCheckerFunc(func(ctx context.Context, email string) (bool, error) {
			return memberpostgres.PublicEmailCodeRegistrationBlocked(ctx, deps.db, email)
		}),
	)
	if err != nil {
		return fmt.Errorf("initialize unified authentication handler: %w", err)
	}
	mux.Handle("/login", authenticationAccess.Wrap(unifiedAuth))
	mux.Handle("/login/", authenticationAccess.Wrap(unifiedAuth))
	return nil
}

func logApplicationDependenciesReady(cfg *config.Config) {
	slog.Info("Application dependencies initialized",
		"s3Bucket", cfg.S3Bucket,
		"s3Region", cfg.S3Region,
		"instanceId", cfg.InstanceID,
	)
}

func (d *applicationDependencies) Close() {
	if d.spicedbClient != nil {
		if err := d.spicedbClient.Close(); err != nil {
			slog.Warn("Failed to close SpiceDB client", "failure_cause", failurecause.Message(err))
		}
	}
	closePublisher(d.workerPublisher)
	closePublisher(d.servicePublisher)
	closePublisher(d.ogPublisher)
	closePublisher(d.hooksPublisher)
	if d.sqlDB != nil {
		if err := d.sqlDB.Close(); err != nil {
			slog.Error("Database close failed", "failure_cause", failurecause.Message(err))
		}
	}
}

func closePublisher(publisher *mq.Publisher) {
	if publisher != nil {
		_ = publisher.Close()
	}
}
