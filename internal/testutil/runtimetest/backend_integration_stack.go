//go:build integration

package runtimetest

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	orytest "github.com/echovisionlab/geul-api/internal/testutil/orytest"
	pgtest "github.com/echovisionlab/geul-api/internal/testutil/pgtest"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	IntegrationTokenSigningSecret = "backend-integration-token-signing-secret"
	IntegrationMediaSigningSecret = "backend-integration-media-signing-secret"
	// The shared backend stack runs auth setup, applies app schema SQL, and starts all service containers.
	// Keep this separate from the smaller Ory-only helper timeout.
	backendIntegrationStackSetupTimeout = 10 * time.Minute
	backendIntegrationCleanupTimeout    = 30 * time.Second
)

type BackendIntegrationStack struct {
	Postgres *pgtest.AppPostgres
	Network  *testcontainers.DockerNetwork

	KratosAdminURL     string
	KratosPublicURL    string
	OathkeeperProxyURL string
	OathkeeperAdminURL string
	SpiceDBEndpoint    string
	SpiceDBToken       string
	TokenSigningSecret string
	MediaSigningSecret string

	S3Region          string
	S3Endpoint        string
	S3AccessKeyID     string
	S3SecretAccessKey string
	S3MediaBucket     string
	S3CacheBucket     string
	S3ForcePathStyle  bool

	KratosClient  *auth.KratosClient
	SpiceDBClient *auth.SpiceDBClient

	// The isolation templates are set on the orchestrator-owned stack only.
	PostgresIsolationTemplate   string
	PostgresLocalKratosTemplate string

	cleanups []*pgtest.IntegrationCleanup
}

type BackendIntegrationStackOptions struct {
	BrowserBaseURL string
	HookBaseURL    string
	Logf           func(format string, args ...structured.Value)
}

func StartBackendIntegrationStack(ctx context.Context) (*BackendIntegrationStack, error) {
	return StartBackendIntegrationStackWithOptions(ctx, BackendIntegrationStackOptions{})
}

func StartBackendIntegrationStackWithOptions(ctx context.Context, opts BackendIntegrationStackOptions) (*BackendIntegrationStack, error) {
	ctx, cancel := context.WithTimeout(ctx, backendIntegrationStackSetupTimeout)
	defer cancel()
	lease, err := pgtest.LoadAppIntegrationLease(pgtest.CurrentAppIntegrationLeasePath())
	if err != nil {
		return nil, err
	}
	if lease.Backend != nil {
		return connectSharedBackendIntegrationStack(ctx, lease, opts)
	}

	stack := &BackendIntegrationStack{
		TokenSigningSecret: IntegrationTokenSigningSecret,
		MediaSigningSecret: IntegrationMediaSigningSecret,
	}
	var netw *testcontainers.DockerNetwork
	if err := backendIntegrationStartupStep(ctx, opts.Logf, "create docker network", func(ctx context.Context) error {
		var err error
		netw, err = network.New(ctx)
		if err != nil {
			return fmt.Errorf("create backend integration network: %w", err)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	stack.Network = netw
	stack.addCleanup("backend integration network", func() error {
		return runBackendIntegrationBoundedCleanup(func(ctx context.Context) error {
			return netw.Remove(ctx)
		})
	})

	var pg *pgtest.AppPostgres
	var pgCleanup func() error
	if err := backendIntegrationStartupStep(ctx, opts.Logf, "start postgres", func(ctx context.Context) error {
		var err error
		pg, pgCleanup, err = pgtest.StartAppPostgres(ctx, pgtest.AppPostgresOptions{
			NetworkID:           netw.ID,
			Aliases:             []string{"postgres"},
			BootstrapKratosStub: false,
			ApplyAppSchemaSQL:   false,
		})
		if err != nil {
			return fmt.Errorf("start backend integration postgres: %w", err)
		}
		return nil
	}); err != nil {
		_ = stack.Close()
		return nil, err
	}
	stack.Postgres = pg
	stack.addCleanup("backend integration postgres", pgCleanup)

	if err := backendIntegrationStartupStep(ctx, opts.Logf, "create auth schemas", func(ctx context.Context) error {
		if _, err := pg.SQLDB.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS kratos;`); err != nil {
			return fmt.Errorf("create backend integration auth schemas: %w", err)
		}
		return nil
	}); err != nil {
		_ = stack.Close()
		return nil, err
	}

	kratosInternalDSN, err := pg.InternalDSN("postgres")
	if err != nil {
		_ = stack.Close()
		return nil, err
	}
	kratosInternalDSN += "&search_path=kratos,public"

	if err := backendIntegrationStartupStep(ctx, opts.Logf, "run kratos migrations", func(ctx context.Context) error {
		return runBackendIntegrationKratosMigrate(ctx, netw, kratosInternalDSN)
	}); err != nil {
		_ = stack.Close()
		return nil, err
	}
	if err := backendIntegrationStartupStep(ctx, opts.Logf, "verify kratos schema", func(ctx context.Context) error {
		return pgtest.AppIntegrationRelationExists(ctx, pg.SQLDB, "kratos.identities")
	}); err != nil {
		_ = stack.Close()
		return nil, err
	}
	if err := backendIntegrationStartupStep(ctx, opts.Logf, "apply app schema SQL", func(context.Context) error {
		if err := pgtest.ApplyAppIntegrationSchemaSQL(pg.SQLDB); err != nil {
			return fmt.Errorf("apply app schema SQL: %w", err)
		}
		return nil
	}); err != nil {
		_ = stack.Close()
		return nil, err
	}
	var kratosCleanup func() error
	if err := backendIntegrationStartupStep(ctx, opts.Logf, "start kratos", func(ctx context.Context) error {
		var err error
		stack.KratosAdminURL, stack.KratosPublicURL, kratosCleanup, err = runBackendIntegrationKratos(
			ctx,
			netw,
			kratosInternalDSN,
			opts.BrowserBaseURL,
			opts.HookBaseURL,
		)
		return err
	}); err != nil {
		_ = stack.Close()
		return nil, err
	}
	stack.KratosClient = auth.NewKratosClient(stack.KratosAdminURL)
	stack.addCleanup("backend integration kratos", kratosCleanup)

	if err := backendIntegrationStartupStep(ctx, opts.Logf, "prepare isolation template", func(ctx context.Context) error {
		var err error
		stack.PostgresIsolationTemplate, err = pgtest.PrepareBackendIsolationTemplate(ctx, lease, pg.DatabaseName)
		if err != nil {
			return err
		}
		stack.PostgresLocalKratosTemplate, err = pgtest.PrepareLocalKratosTemplate(ctx, lease, pg.DatabaseName)
		return err
	}); err != nil {
		_ = stack.Close()
		return nil, err
	}

	var spiceDBCleanup func() error
	if err := backendIntegrationStartupStep(ctx, opts.Logf, "start SpiceDB", func(ctx context.Context) error {
		var err error
		stack.SpiceDBEndpoint, stack.SpiceDBToken, stack.SpiceDBClient, spiceDBCleanup, err = runBackendIntegrationSpiceDB(ctx, netw)
		return err
	}); err != nil {
		_ = stack.Close()
		return nil, err
	}
	stack.addCleanup("backend integration SpiceDB", spiceDBCleanup)

	var oathkeeperCleanup func() error
	if err := backendIntegrationStartupStep(ctx, opts.Logf, "start Oathkeeper", func(ctx context.Context) error {
		var err error
		stack.OathkeeperProxyURL, stack.OathkeeperAdminURL, oathkeeperCleanup, err = runBackendIntegrationOathkeeper(ctx, netw)
		return err
	}); err != nil {
		_ = stack.Close()
		return nil, err
	}
	stack.addCleanup("backend integration Oathkeeper", oathkeeperCleanup)

	var minioCleanup func() error
	var s3Endpoint, mediaBucket, cacheBucket string
	if err := backendIntegrationStartupStep(ctx, opts.Logf, "start minio", func(ctx context.Context) error {
		var err error
		s3Endpoint, mediaBucket, cacheBucket, minioCleanup, err = startBackendIntegrationMinIO(ctx)
		return err
	}); err != nil {
		_ = stack.Close()
		return nil, err
	}
	stack.S3Region = "us-east-1"
	stack.S3Endpoint = s3Endpoint
	stack.S3AccessKeyID = "minioadmin"
	stack.S3SecretAccessKey = "minioadmin"
	stack.S3MediaBucket = mediaBucket
	stack.S3CacheBucket = cacheBucket
	stack.S3ForcePathStyle = true
	stack.addCleanup("backend integration minio", minioCleanup)

	return stack, nil
}

// connectSharedBackendIntegrationStack gives the caller its own database,
// SpiceDB datastore and buckets on the orchestrator-owned services.
func connectSharedBackendIntegrationStack(ctx context.Context, lease pgtest.AppIntegrationLeaseDescriptor, opts BackendIntegrationStackOptions) (*BackendIntegrationStack, error) {
	postgres, postgresCleanup, err := pgtest.OpenIsolatedAppPostgres(ctx, lease)
	if err != nil {
		return nil, err
	}
	backend := lease.Backend
	stack := &BackendIntegrationStack{
		Postgres:           postgres,
		KratosAdminURL:     backend.KratosAdminURL,
		KratosPublicURL:    backend.KratosPublicURL,
		OathkeeperAdminURL: backend.OathkeeperAdminURL,
		OathkeeperProxyURL: backend.OathkeeperProxyURL,
		SpiceDBEndpoint:    backend.SpiceDBEndpoint,
		TokenSigningSecret: IntegrationTokenSigningSecret,
		MediaSigningSecret: IntegrationMediaSigningSecret,
		S3Region:           backend.S3Region,
		S3Endpoint:         backend.S3Endpoint,
		S3AccessKeyID:      backend.S3AccessKeyID,
		S3SecretAccessKey:  backend.S3SecretAccessKey,
		S3ForcePathStyle:   backend.S3ForcePathStyle,
		KratosClient:       auth.NewKratosClient(backend.KratosAdminURL),
	}
	stack.addCleanup("isolated backend database", postgresCleanup)
	stack.SpiceDBToken, stack.SpiceDBClient, err = orytest.OpenIsolatedSpiceDB(ctx, backend.SpiceDBEndpoint)
	if err != nil {
		_ = stack.Close()
		return nil, err
	}
	stack.addCleanup("isolated backend SpiceDB client", stack.SpiceDBClient.Close)
	stack.S3MediaBucket, stack.S3CacheBucket, err = openIsolatedS3Buckets(ctx, backend)
	if err != nil {
		_ = stack.Close()
		return nil, err
	}
	if strings.TrimSpace(opts.HookBaseURL) != "" {
		unregister, err := orytest.RegisterSuiteHookUpstream(ctx, backend, opts.HookBaseURL)
		if err != nil {
			_ = stack.Close()
			return nil, err
		}
		stack.addCleanup("suite backend hook upstream", func() error {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), backendIntegrationCleanupTimeout)
			defer cancel()
			return unregister(cleanupCtx)
		})
	}
	return stack, nil
}

func backendIntegrationStartupStep(
	ctx context.Context,
	logf func(format string, args ...structured.Value),
	name string,
	fn func(context.Context) error,
) error {
	if logf != nil {
		logf("backend integration stack: starting %s", name)
	}
	err := fn(ctx)
	if err != nil {
		if logf != nil {
			logf("backend integration stack: failed %s: %v", name, err)
		}
		return err
	}
	if logf != nil {
		logf("backend integration stack: finished %s", name)
	}
	return nil
}

func runBackendIntegrationBoundedCleanup(cleanup func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), backendIntegrationCleanupTimeout)
	defer cancel()
	return cleanup(ctx)
}

func (s *BackendIntegrationStack) Lease() *pgtest.AppIntegrationBackendLease {
	return &pgtest.AppIntegrationBackendLease{
		PostgresDSN:                 s.Postgres.DSN,
		PostgresDatabaseName:        s.Postgres.DatabaseName,
		PostgresIsolationTemplate:   s.PostgresIsolationTemplate,
		PostgresLocalKratosTemplate: s.PostgresLocalKratosTemplate,
		KratosAdminURL:              s.KratosAdminURL,
		KratosPublicURL:             s.KratosPublicURL,
		OathkeeperAdminURL:          s.OathkeeperAdminURL,
		OathkeeperProxyURL:          s.OathkeeperProxyURL,
		SpiceDBEndpoint:             s.SpiceDBEndpoint,
		SpiceDBToken:                s.SpiceDBToken,
		S3Region:                    s.S3Region,
		S3Endpoint:                  s.S3Endpoint,
		S3AccessKeyID:               s.S3AccessKeyID,
		S3SecretAccessKey:           s.S3SecretAccessKey,
		S3MediaBucket:               s.S3MediaBucket,
		S3CacheBucket:               s.S3CacheBucket,
		S3ForcePathStyle:            s.S3ForcePathStyle,
	}
}

func (s *BackendIntegrationStack) Close() error {
	var firstErr error
	for i := len(s.cleanups) - 1; i >= 0; i-- {
		if err := s.cleanups[i].Run(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *BackendIntegrationStack) addCleanup(name string, fn func() error) {
	cleanup := pgtest.NewIntegrationCleanup(name, fn)
	s.cleanups = append(s.cleanups, cleanup)
	pgtest.RegisterIntegrationProcessCleanup(name, cleanup.Run)
}

func runBackendIntegrationKratosMigrate(ctx context.Context, netw *testcontainers.DockerNetwork, dsn string) error {
	ctr, err := testcontainers.Run(ctx, orytest.KratosIntegrationImage(), network.WithNetwork([]string{"kratos-migrate"}, netw),
		testcontainers.WithEnv(map[string]string{
			"DSN": dsn,
		}),
		testcontainers.WithCmd("migrate", "sql", "up", "-e", "--yes"),
		testcontainers.WithWaitStrategy(wait.ForExit()),
	)
	if err != nil {
		return fmt.Errorf("start kratos migrate container: %w", err)
	}
	defer func() {
		_ = runBackendIntegrationBoundedCleanup(func(ctx context.Context) error {
			return ctr.Terminate(ctx)
		})
	}()

	state, err := ctr.State(ctx)
	if err != nil {
		return fmt.Errorf("inspect kratos migrate container: %w", err)
	}
	if state == nil || state.ExitCode != 0 {
		exitCode := -1
		if state != nil {
			exitCode = state.ExitCode
		}
		logs := backendIntegrationContainerLogs(ctx, ctr)
		return fmt.Errorf("kratos migrate exited with code %d%s", exitCode, logs)
	}
	return nil
}

func runBackendIntegrationKratos(
	ctx context.Context,
	netw *testcontainers.DockerNetwork,
	dsn string,
	browserBaseURL string,
	hookBaseURL string,
) (adminURL, publicURL string, cleanup func() error, err error) {
	browserOrigin := "http://127.0.0.1:3000"
	hookOrigin := "http://127.0.0.1:65535"
	if trimmed := strings.TrimSpace(browserBaseURL); trimmed != "" {
		parsed, err := url.Parse(trimmed)
		if err != nil {
			return "", "", nil, fmt.Errorf("parse kratos browser base url: %w", err)
		}
		if parsed.Scheme == "" || parsed.Host == "" {
			return "", "", nil, fmt.Errorf("parse kratos browser base url: missing scheme or host")
		}
		browserOrigin = strings.TrimRight(trimmed, "/")
	}
	if trimmed := strings.TrimSpace(hookBaseURL); trimmed != "" {
		parsed, err := url.Parse(trimmed)
		if err != nil {
			return "", "", nil, fmt.Errorf("parse kratos hook base url: %w", err)
		}
		if parsed.Scheme == "" || parsed.Host == "" {
			return "", "", nil, fmt.Errorf("parse kratos hook base url: missing scheme or host")
		}
		hookOrigin = strings.TrimRight(trimmed, "/")
	}

	kratosConfigOverrides := make(map[string]string)
	applyKratosConfigOverrides(kratosConfigOverrides, browserOrigin)
	kratosOptions := []testcontainers.ContainerCustomizer{
		network.WithNetwork([]string{"kratos", "geul-identity-kratos-prod"}, netw),
		testcontainers.WithFiles(backendIntegrationContainerFiles("../../../../infra/kratos", "/etc/config/kratos")...),
		testcontainers.WithExposedPorts("4433/tcp", "4434/tcp"),
		testcontainers.WithEnv(map[string]string{
			"DSN":                                    dsn,
			"KRATOS_ADMIN_URL":                       "http://kratos:4434",
			"KRATOS_PASSKEY_RP_ID":                   "127.0.0.1",
			"TOKEN_SIGNING_SECRET":                   IntegrationTokenSigningSecret,
			"INTERNAL_SERVICE_HEADER_NAME":           "X-Internal-Service",
			"SECRETS_COOKIE_0":                       "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"SECRETS_CIPHER_0":                       "0123456789abcdef0123456789abcdef",
			"CIPHERS_ALGORITHM":                      "xchacha20-poly1305",
			"SERVE_PUBLIC_BASE_URL":                  "http://kratos:4433",
			"SERVE_PUBLIC_CORS_ALLOWED_ORIGINS_0":    browserOrigin,
			"SITE_ORIGIN":                            browserOrigin,
			"SESSION_COOKIE_NAME":                    orytest.OryTestSessionCookieName,
			"SELFSERVICE_DEFAULT_BROWSER_RETURN_URL": browserOrigin,
			"SELFSERVICE_ALLOWED_RETURN_URLS_0":      browserOrigin,
			"SELFSERVICE_ALLOWED_RETURN_URLS_1":      browserOrigin + "/my/security",
			"SELFSERVICE_FLOWS_LOGIN_UI_URL":         browserOrigin + "/login",
			"SELFSERVICE_FLOWS_REGISTRATION_UI_URL":  browserOrigin + "/login",
			"SELFSERVICE_FLOWS_LOGOUT_AFTER_DEFAULT_BROWSER_RETURN_URL":                     browserOrigin,
			"SELFSERVICE_FLOWS_VERIFICATION_UI_URL":                                         browserOrigin + "/verify",
			"SELFSERVICE_FLOWS_VERIFICATION_AFTER_DEFAULT_BROWSER_RETURN_URL":               browserOrigin,
			"SELFSERVICE_FLOWS_SETTINGS_UI_URL":                                             browserOrigin + "/my/settings",
			"SELFSERVICE_FLOWS_ERROR_UI_URL":                                                browserOrigin + "/login/error",
			"COURIER_HTTP_REQUEST_CONFIG_URL":                                               hookOrigin + "/api.intra.v1.EmailCourierService/SendEmail",
			"COURIER_HTTP_REQUEST_CONFIG_AUTH_CONFIG_VALUE":                                 IntegrationTokenSigningSecret,
			"SELFSERVICE_FLOWS_LOGIN_AFTER_OIDC_HOOKS_0_CONFIG_URL":                         hookOrigin + "/hooks/after-login",
			"SELFSERVICE_FLOWS_LOGIN_AFTER_OIDC_HOOKS_0_CONFIG_AUTH_CONFIG_VALUE":           IntegrationTokenSigningSecret,
			"SELFSERVICE_FLOWS_LOGIN_AFTER_CODE_HOOKS_0_CONFIG_URL":                         hookOrigin + "/hooks/after-login",
			"SELFSERVICE_FLOWS_LOGIN_AFTER_CODE_HOOKS_0_CONFIG_AUTH_CONFIG_VALUE":           IntegrationTokenSigningSecret,
			"SELFSERVICE_FLOWS_LOGIN_AFTER_PASSKEY_HOOKS_0_CONFIG_URL":                      hookOrigin + "/hooks/after-login",
			"SELFSERVICE_FLOWS_LOGIN_AFTER_PASSKEY_HOOKS_0_CONFIG_AUTH_CONFIG_VALUE":        IntegrationTokenSigningSecret,
			"SELFSERVICE_FLOWS_REGISTRATION_AFTER_OIDC_HOOKS_0_CONFIG_URL":                  hookOrigin + "/hooks/reject-credential-registration",
			"SELFSERVICE_FLOWS_REGISTRATION_AFTER_OIDC_HOOKS_0_CONFIG_AUTH_CONFIG_VALUE":    IntegrationTokenSigningSecret,
			"SELFSERVICE_FLOWS_REGISTRATION_AFTER_OIDC_HOOKS_1_CONFIG_URL":                  hookOrigin + "/hooks/after-login",
			"SELFSERVICE_FLOWS_REGISTRATION_AFTER_OIDC_HOOKS_1_CONFIG_AUTH_CONFIG_VALUE":    IntegrationTokenSigningSecret,
			"SELFSERVICE_FLOWS_REGISTRATION_AFTER_CODE_HOOKS_0_CONFIG_URL":                  hookOrigin + "/hooks/reject-credential-registration",
			"SELFSERVICE_FLOWS_REGISTRATION_AFTER_CODE_HOOKS_0_CONFIG_AUTH_CONFIG_VALUE":    IntegrationTokenSigningSecret,
			"SELFSERVICE_FLOWS_REGISTRATION_AFTER_CODE_HOOKS_1_CONFIG_URL":                  hookOrigin + "/hooks/after-login",
			"SELFSERVICE_FLOWS_REGISTRATION_AFTER_CODE_HOOKS_1_CONFIG_AUTH_CONFIG_VALUE":    IntegrationTokenSigningSecret,
			"SELFSERVICE_FLOWS_REGISTRATION_AFTER_PASSKEY_HOOKS_0_CONFIG_URL":               hookOrigin + "/hooks/reject-credential-registration",
			"SELFSERVICE_FLOWS_REGISTRATION_AFTER_PASSKEY_HOOKS_0_CONFIG_AUTH_CONFIG_VALUE": IntegrationTokenSigningSecret,
			"SELFSERVICE_FLOWS_VERIFICATION_AFTER_HOOKS_0_CONFIG_URL":                       hookOrigin + "/hooks/after-verification",
			"SELFSERVICE_FLOWS_VERIFICATION_AFTER_HOOKS_0_CONFIG_AUTH_CONFIG_VALUE":         IntegrationTokenSigningSecret,
			"SELFSERVICE_FLOWS_SETTINGS_AFTER_OIDC_HOOKS_0_CONFIG_URL":                      hookOrigin + "/hooks/pre-settings-oidc",
			"SELFSERVICE_FLOWS_SETTINGS_AFTER_OIDC_HOOKS_0_CONFIG_AUTH_CONFIG_VALUE":        IntegrationTokenSigningSecret,
			"SELFSERVICE_FLOWS_SETTINGS_AFTER_OIDC_HOOKS_1_CONFIG_URL":                      hookOrigin + "/hooks/post-settings-oidc",
			"SELFSERVICE_FLOWS_SETTINGS_AFTER_OIDC_HOOKS_1_CONFIG_AUTH_CONFIG_VALUE":        IntegrationTokenSigningSecret,
			"SELFSERVICE_FLOWS_SETTINGS_AFTER_PASSKEY_HOOKS_0_CONFIG_URL":                   hookOrigin + "/hooks/pre-settings-passkey",
			"SELFSERVICE_FLOWS_SETTINGS_AFTER_PASSKEY_HOOKS_0_CONFIG_AUTH_CONFIG_VALUE":     IntegrationTokenSigningSecret,
			"SELFSERVICE_FLOWS_SETTINGS_AFTER_PASSKEY_HOOKS_1_CONFIG_URL":                   hookOrigin + "/hooks/post-settings-passkey",
			"SELFSERVICE_FLOWS_SETTINGS_AFTER_PASSKEY_HOOKS_1_CONFIG_AUTH_CONFIG_VALUE":     IntegrationTokenSigningSecret,
			"SELFSERVICE_FLOWS_SETTINGS_AFTER_PROFILE_HOOKS_0_CONFIG_URL":                   hookOrigin + "/hooks/after-settings",
			"SELFSERVICE_FLOWS_SETTINGS_AFTER_PROFILE_HOOKS_0_CONFIG_AUTH_CONFIG_VALUE":     IntegrationTokenSigningSecret,
			"SELFSERVICE_METHODS_OIDC_CONFIG_PROVIDERS_0_CLIENT_ID":                         "dummy-google-client-id",
			"SELFSERVICE_METHODS_OIDC_CONFIG_PROVIDERS_0_CLIENT_SECRET":                     "dummy-google-client-secret",
			"SELFSERVICE_METHODS_OIDC_CONFIG_PROVIDERS_1_CLIENT_ID":                         "dummy-github-client-id",
			"SELFSERVICE_METHODS_OIDC_CONFIG_PROVIDERS_1_CLIENT_SECRET":                     "dummy-github-client-secret",
		}),
		testcontainers.WithEnv(kratosConfigOverrides),
		testcontainers.WithCmd(
			"serve",
			"-c",
			"/etc/config/kratos/kratos.yml",
			"--dev",
			"--watch-courier",
		),
		testcontainers.WithWaitStrategy(wait.ForAll(
			wait.ForListeningPort("4433/tcp"),
			wait.ForListeningPort("4434/tcp"),
		)),
	}
	hostAccessOptions, err := hostAccessOptionsForURL(hookOrigin)
	if err != nil {
		return "", "", nil, fmt.Errorf("configure kratos host access: %w", err)
	}
	kratosOptions = append(kratosOptions, hostAccessOptions...)

	ctr, err := testcontainers.Run(ctx, orytest.KratosIntegrationImage(), kratosOptions...)
	if err != nil {
		return "", "", nil, fmt.Errorf("start kratos container: %w", err)
	}
	cleanup = func() error {
		return runBackendIntegrationBoundedCleanup(func(ctx context.Context) error {
			return ctr.Terminate(ctx)
		})
	}

	adminPort, err := ctr.MappedPort(ctx, "4434/tcp")
	if err != nil {
		_ = cleanup()
		return "", "", nil, fmt.Errorf("resolve kratos admin port: %w", err)
	}
	publicPort, err := ctr.MappedPort(ctx, "4433/tcp")
	if err != nil {
		_ = cleanup()
		return "", "", nil, fmt.Errorf("resolve kratos public port: %w", err)
	}

	adminURL = "http://" + orytest.OryHostLoopback + ":" + adminPort.Port()
	publicURL = "http://" + orytest.OryHostLoopback + ":" + publicPort.Port()
	if err := backendIntegrationWaitHTTPReady(ctx, adminURL+"/health/ready"); err != nil {
		_ = cleanup()
		return "", "", nil, fmt.Errorf("wait for kratos readiness: %w", err)
	}

	return adminURL, publicURL, cleanup, nil
}

func startBackendIntegrationMinIO(ctx context.Context) (endpoint, mediaBucket, cacheBucket string, cleanup func() error, err error) {
	ctr, err := testcontainers.Run(ctx, pgtest.MinIOIntegrationImage(), // Alternate images may run as non-root and omit a shell.
		testcontainers.WithTmpfs(map[string]string{"/data": "rw,mode=1777"}),
		testcontainers.WithExposedPorts("9000/tcp"),
		testcontainers.WithEnv(map[string]string{
			"MINIO_ROOT_USER":     "minioadmin",
			"MINIO_ROOT_PASSWORD": "minioadmin",
		}),
		testcontainers.WithCmd("server", "/data"),
		testcontainers.WithWaitStrategy(
			wait.ForAll(
				wait.ForListeningPort("9000/tcp").SkipInternalCheck(),
				wait.ForHTTP("/minio/health/live").WithPort("9000/tcp"),
			).WithDeadline(2*time.Minute),
		),
	)
	if err != nil {
		return "", "", "", nil, fmt.Errorf("start minio container: %w", err)
	}
	cleanup = func() error {
		return runBackendIntegrationBoundedCleanup(func(ctx context.Context) error {
			return ctr.Terminate(ctx)
		})
	}

	host, err := ctr.Host(ctx)
	if err != nil {
		_ = cleanup()
		return "", "", "", nil, fmt.Errorf("resolve minio host: %w", err)
	}
	port, err := ctr.MappedPort(ctx, "9000/tcp")
	if err != nil {
		_ = cleanup()
		return "", "", "", nil, fmt.Errorf("resolve minio port: %w", err)
	}
	endpoint = fmt.Sprintf("http://%s:%s", host, port.Port())
	mediaBucket = "integration-media"
	cacheBucket = "integration-cache"

	if err := createBackendIntegrationBuckets(ctx, endpoint, mediaBucket, cacheBucket); err != nil {
		_ = cleanup()
		return "", "", "", nil, err
	}
	return endpoint, mediaBucket, cacheBucket, cleanup, nil
}

func createBackendIntegrationBuckets(ctx context.Context, endpoint string, buckets ...string) error {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("minioadmin", "minioadmin", "")),
	)
	if err != nil {
		return fmt.Errorf("load minio aws config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
	for _, bucket := range buckets {
		if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
			return fmt.Errorf("create minio bucket %s: %w", bucket, err)
		}
	}
	return nil
}

func backendIntegrationContainerFiles(relativeDir string, containerDir string) []testcontainers.ContainerFile {
	hostDir := pgtest.AppIntegrationRepoPath(relativeDir)
	containerDir = strings.TrimRight(containerDir, "/")
	files := make([]testcontainers.ContainerFile, 0)
	err := filepath.WalkDir(hostDir, func(filePath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}

		relativePath, err := filepath.Rel(hostDir, filePath)
		if err != nil {
			return err
		}
		files = append(files, testcontainers.ContainerFile{
			HostFilePath:      filePath,
			ContainerFilePath: containerDir + "/" + filepath.ToSlash(relativePath),
			FileMode:          0o644,
		})
		return nil
	})
	if err != nil {
		panic(err)
	}
	return files
}

func backendIntegrationWaitHTTPReady(ctx context.Context, endpoint string) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			if err != nil {
				return err
			}
			return fmt.Errorf("readiness endpoint %s did not return 200", endpoint)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func backendIntegrationContainerLogs(ctx context.Context, ctr testcontainers.Container) string {
	reader, err := ctr.Logs(ctx)
	if err != nil {
		return ""
	}
	defer reader.Close()
	logs, err := io.ReadAll(reader)
	if err != nil || len(logs) == 0 {
		return ""
	}
	return ": " + string(logs)
}
