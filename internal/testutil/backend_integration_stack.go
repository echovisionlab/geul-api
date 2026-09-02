//go:build integration

package testutil

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/structured"
)

const (
	IntegrationTokenSigningSecret = "backend-integration-token-signing-secret"
	IntegrationMediaSigningSecret = "backend-integration-media-signing-secret"
	// The shared backend stack runs auth setup, applies app schema SQL, and starts all service containers.
	// Keep this separate from the smaller Ory-only helper timeout.
	backendIntegrationStackSetupTimeout = 10 * time.Minute
	backendIntegrationCleanupTimeout    = 30 * time.Second
	backendIntegrationS3ResetTimeout    = 30 * time.Second
)

type backendIntegrationS3ResetClient interface {
	ListMultipartUploads(context.Context, *s3.ListMultipartUploadsInput, ...func(*s3.Options)) (*s3.ListMultipartUploadsOutput, error)
	AbortMultipartUpload(context.Context, *s3.AbortMultipartUploadInput, ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error)
	ListObjectVersions(context.Context, *s3.ListObjectVersionsInput, ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error)
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	DeleteObjects(context.Context, *s3.DeleteObjectsInput, ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
}

type BackendIntegrationStack struct {
	Postgres *AppPostgres
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

	postgresBaseline *IntegrationDatabaseBaseline
	cleanups         []*integrationCleanup
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
	lease, err := loadAppIntegrationLease(currentAppIntegrationLeasePath())
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

	var pg *AppPostgres
	var pgCleanup func() error
	if err := backendIntegrationStartupStep(ctx, opts.Logf, "start postgres", func(ctx context.Context) error {
		var err error
		pg, pgCleanup, err = StartAppPostgres(ctx, AppPostgresOptions{
			Network:             netw,
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
		return appIntegrationRelationExists(ctx, pg.SQLDB, "kratos.identities")
	}); err != nil {
		_ = stack.Close()
		return nil, err
	}
	if err := backendIntegrationStartupStep(ctx, opts.Logf, "apply app schema SQL", func(context.Context) error {
		if err := applyAppIntegrationSchemaSQL(pg.SQLDB); err != nil {
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

	// Kratos creates its runtime network row during startup. Capture only after
	// every service is ready so restoring the baseline preserves that required
	// runtime seed as well as schema bootstrap data.
	if err := backendIntegrationStartupStep(ctx, opts.Logf, "capture postgres baseline", func(ctx context.Context) error {
		baseline, err := CaptureIntegrationDatabaseBaseline(ctx, pg, pg.DB)
		if err != nil {
			return err
		}
		stack.postgresBaseline = baseline
		stack.addCleanup("backend integration postgres baseline", baseline.Close)
		return nil
	}); err != nil {
		_ = stack.Close()
		return nil, err
	}

	return stack, nil
}

func connectSharedBackendIntegrationStack(ctx context.Context, lease AppIntegrationLeaseDescriptor, opts BackendIntegrationStackOptions) (*BackendIntegrationStack, error) {
	postgres, postgresCleanup, err := connectSharedAppPostgres(ctx, lease)
	if err != nil {
		return nil, err
	}
	backend := lease.Backend
	spiceDBClient, err := auth.NewSpiceDBClient(backend.SpiceDBEndpoint, backend.SpiceDBToken, true)
	if err != nil {
		_ = postgresCleanup()
		return nil, fmt.Errorf("connect suite backend SpiceDB: %w", err)
	}
	stack := &BackendIntegrationStack{
		Postgres:           postgres,
		KratosAdminURL:     backend.KratosAdminURL,
		KratosPublicURL:    backend.KratosPublicURL,
		OathkeeperAdminURL: backend.OathkeeperAdminURL,
		OathkeeperProxyURL: backend.OathkeeperProxyURL,
		SpiceDBEndpoint:    backend.SpiceDBEndpoint,
		SpiceDBToken:       backend.SpiceDBToken,
		TokenSigningSecret: IntegrationTokenSigningSecret,
		MediaSigningSecret: IntegrationMediaSigningSecret,
		S3Region:           backend.S3Region,
		S3Endpoint:         backend.S3Endpoint,
		S3AccessKeyID:      backend.S3AccessKeyID,
		S3SecretAccessKey:  backend.S3SecretAccessKey,
		S3MediaBucket:      backend.S3MediaBucket,
		S3CacheBucket:      backend.S3CacheBucket,
		S3ForcePathStyle:   backend.S3ForcePathStyle,
		KratosClient:       auth.NewKratosClient(backend.KratosAdminURL),
		SpiceDBClient:      spiceDBClient,
	}
	stack.addCleanup("suite backend database", postgresCleanup)
	stack.addCleanup("suite backend SpiceDB client", spiceDBClient.Close)
	if strings.TrimSpace(opts.HookBaseURL) != "" {
		unregister, err := registerSuiteHookUpstream(ctx, backend, opts.HookBaseURL)
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
	if err := ResetBackendIntegrationExternalState(ctx, stack); err != nil {
		_ = stack.Close()
		return nil, err
	}
	baseline, err := CaptureIntegrationDatabaseBaseline(ctx, postgres, postgres.DB)
	if err != nil {
		_ = stack.Close()
		return nil, err
	}
	stack.postgresBaseline = baseline
	stack.addCleanup("suite backend postgres baseline", baseline.Close)
	stack.addCleanup("suite backend state", func() error {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), backendIntegrationCleanupTimeout)
		defer cancel()
		return errors.Join(
			baseline.Restore(cleanupCtx),
			ResetBackendIntegrationExternalState(cleanupCtx, stack),
		)
	})
	return stack, nil
}

func registerSuiteHookUpstream(ctx context.Context, backend *AppIntegrationBackendLease, rawUpstream string) (func(context.Context) error, error) {
	upstream, err := url.Parse(strings.TrimSpace(rawUpstream))
	if err != nil {
		return nil, fmt.Errorf("parse suite hook upstream: %w", err)
	}
	if upstream.Hostname() == testcontainers.HostInternal {
		upstream.Host = "127.0.0.1:" + upstream.Port()
	}
	payload, err := json.Marshal(map[string]string{"upstream_base_url": upstream.String()})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, backend.HookControlURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+backend.HookControlToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("register suite hook upstream: %w", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return nil, fmt.Errorf("register suite hook upstream: status %d", response.StatusCode)
	}
	registration := response.Header.Get("Integration-Hook-Registration")
	if strings.TrimSpace(registration) == "" {
		return nil, fmt.Errorf("register suite hook upstream: missing registration")
	}
	return func(ctx context.Context) error {
		request, err := http.NewRequestWithContext(ctx, http.MethodDelete, backend.HookControlURL, nil)
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer "+backend.HookControlToken)
		request.Header.Set("Integration-Hook-Registration", registration)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return err
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			return fmt.Errorf("unregister suite hook upstream: status %d", response.StatusCode)
		}
		return nil
	}, nil
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

func (s *BackendIntegrationStack) Lease() *AppIntegrationBackendLease {
	return &AppIntegrationBackendLease{
		PostgresDSN:          s.Postgres.DSN,
		PostgresDatabaseName: s.Postgres.DatabaseName,
		KratosAdminURL:       s.KratosAdminURL,
		KratosPublicURL:      s.KratosPublicURL,
		OathkeeperAdminURL:   s.OathkeeperAdminURL,
		OathkeeperProxyURL:   s.OathkeeperProxyURL,
		SpiceDBEndpoint:      s.SpiceDBEndpoint,
		SpiceDBToken:         s.SpiceDBToken,
		S3Region:             s.S3Region,
		S3Endpoint:           s.S3Endpoint,
		S3AccessKeyID:        s.S3AccessKeyID,
		S3SecretAccessKey:    s.S3SecretAccessKey,
		S3MediaBucket:        s.S3MediaBucket,
		S3CacheBucket:        s.S3CacheBucket,
		S3ForcePathStyle:     s.S3ForcePathStyle,
	}
}

// ResetBackendIntegrationExternalState clears PGMQ and SpiceDB state owned by
// a shared BackendIntegrationStack.
func ResetBackendIntegrationExternalState(ctx context.Context, stack *BackendIntegrationStack) error {
	if stack == nil || stack.Postgres == nil {
		return fmt.Errorf("backend integration stack is not initialized")
	}
	return ResetOryIntegrationState(ctx, &OryStack{
		DB:              stack.Postgres.DB,
		PostgresDSN:     stack.Postgres.DSN,
		KratosAdminURL:  stack.KratosAdminURL,
		KratosPublicURL: stack.KratosPublicURL,
		SpiceDBEndpoint: stack.SpiceDBEndpoint,
		SpiceDBToken:    stack.SpiceDBToken,
		KratosClient:    stack.KratosClient,
		SpiceDBClient:   stack.SpiceDBClient,
	})
}

func resetBackendIntegrationS3State(ctx context.Context, stack *BackendIntegrationStack) error {
	resetCtx, cancel := context.WithTimeout(ctx, backendIntegrationS3ResetTimeout)
	defer cancel()

	awsCfg, err := awsconfig.LoadDefaultConfig(resetCtx,
		awsconfig.WithRegion(stack.S3Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			stack.S3AccessKeyID,
			stack.S3SecretAccessKey,
			"",
		)),
	)
	if err != nil {
		return fmt.Errorf("load backend integration S3 reset config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(stack.S3Endpoint)
		options.UsePathStyle = stack.S3ForcePathStyle
	})
	return resetBackendIntegrationS3Buckets(resetCtx, client, stack.S3MediaBucket, stack.S3CacheBucket)
}

func resetBackendIntegrationS3Buckets(
	ctx context.Context,
	client backendIntegrationS3ResetClient,
	buckets ...string,
) error {
	resetCtx, cancel := context.WithTimeout(ctx, backendIntegrationS3ResetTimeout)
	defer cancel()

	var resetErr error
	for _, bucket := range buckets {
		if strings.TrimSpace(bucket) == "" {
			resetErr = errors.Join(resetErr, fmt.Errorf("reset backend integration S3: bucket name is required"))
			continue
		}
		if err := resetBackendIntegrationS3Bucket(resetCtx, client, bucket); err != nil {
			resetErr = errors.Join(resetErr, fmt.Errorf("reset backend integration S3 bucket %s: %w", bucket, err))
		}
	}
	return resetErr
}

func resetBackendIntegrationS3Bucket(
	ctx context.Context,
	client backendIntegrationS3ResetClient,
	bucket string,
) error {
	return errors.Join(
		abortBackendIntegrationMultipartUploads(ctx, client, bucket),
		deleteBackendIntegrationObjectVersions(ctx, client, bucket),
		deleteBackendIntegrationCurrentObjects(ctx, client, bucket),
	)
}

func abortBackendIntegrationMultipartUploads(
	ctx context.Context,
	client backendIntegrationS3ResetClient,
	bucket string,
) error {
	for {
		output, err := client.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{
			Bucket:     aws.String(bucket),
			MaxUploads: aws.Int32(1000),
		})
		if err != nil {
			return fmt.Errorf("list incomplete multipart uploads: %w", err)
		}
		if len(output.Uploads) == 0 {
			return nil
		}
		for _, upload := range output.Uploads {
			if upload.Key == nil || upload.UploadId == nil {
				return fmt.Errorf("list incomplete multipart uploads returned an incomplete identity")
			}
			if _, err := client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
				Bucket:   aws.String(bucket),
				Key:      upload.Key,
				UploadId: upload.UploadId,
			}); err != nil {
				return fmt.Errorf("abort incomplete multipart upload %s: %w", *upload.Key, err)
			}
		}
	}
}

func deleteBackendIntegrationObjectVersions(
	ctx context.Context,
	client backendIntegrationS3ResetClient,
	bucket string,
) error {
	for {
		output, err := client.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{
			Bucket:  aws.String(bucket),
			MaxKeys: aws.Int32(1000),
		})
		if err != nil {
			return fmt.Errorf("list object versions: %w", err)
		}
		objects := make([]s3types.ObjectIdentifier, 0, len(output.Versions)+len(output.DeleteMarkers))
		for _, version := range output.Versions {
			if version.Key == nil {
				return fmt.Errorf("list object versions returned a missing key")
			}
			objects = append(objects, s3types.ObjectIdentifier{Key: version.Key, VersionId: version.VersionId})
		}
		for _, marker := range output.DeleteMarkers {
			if marker.Key == nil {
				return fmt.Errorf("list object versions returned a delete marker without a key")
			}
			objects = append(objects, s3types.ObjectIdentifier{Key: marker.Key, VersionId: marker.VersionId})
		}
		if len(objects) == 0 {
			return nil
		}
		if err := deleteBackendIntegrationObjects(ctx, client, bucket, objects); err != nil {
			return err
		}
	}
}

func deleteBackendIntegrationCurrentObjects(
	ctx context.Context,
	client backendIntegrationS3ResetClient,
	bucket string,
) error {
	for {
		output, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:  aws.String(bucket),
			MaxKeys: aws.Int32(1000),
		})
		if err != nil {
			return fmt.Errorf("list current objects: %w", err)
		}
		objects := make([]s3types.ObjectIdentifier, 0, len(output.Contents))
		for _, object := range output.Contents {
			if object.Key == nil {
				return fmt.Errorf("list current objects returned a missing key")
			}
			objects = append(objects, s3types.ObjectIdentifier{Key: object.Key})
		}
		if len(objects) == 0 {
			return nil
		}
		if err := deleteBackendIntegrationObjects(ctx, client, bucket, objects); err != nil {
			return err
		}
	}
}

func deleteBackendIntegrationObjects(
	ctx context.Context,
	client backendIntegrationS3ResetClient,
	bucket string,
	objects []s3types.ObjectIdentifier,
) error {
	output, err := client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
		Bucket: aws.String(bucket),
		Delete: &s3types.Delete{Objects: objects, Quiet: aws.Bool(true)},
	})
	if err != nil {
		return fmt.Errorf("delete objects: %w", err)
	}
	if len(output.Errors) != 0 {
		failed := output.Errors[0]
		return fmt.Errorf(
			"delete object %s failed with %s: %s",
			aws.ToString(failed.Key),
			aws.ToString(failed.Code),
			aws.ToString(failed.Message),
		)
	}
	return nil
}

// ResetBackendIntegrationState restores the exact post-schema PostgreSQL
// baseline and clears the stack's non-PostgreSQL authorization state. It is
// intended for serialized tests whose service-owned transactions must commit.
func ResetBackendIntegrationState(ctx context.Context, stack *BackendIntegrationStack) error {
	if stack == nil || stack.Postgres == nil || stack.postgresBaseline == nil {
		return fmt.Errorf("backend integration stack baseline is not initialized")
	}
	var resetErr error
	if err := stack.postgresBaseline.Restore(ctx); err != nil {
		resetErr = errors.Join(resetErr, fmt.Errorf("restore backend integration PostgreSQL baseline: %w", err))
	}
	if err := ResetBackendIntegrationExternalState(ctx, stack); err != nil {
		resetErr = errors.Join(resetErr, fmt.Errorf("reset backend integration external state: %w", err))
	}
	return resetErr
}

// ResetBackendIntegrationPackageState restores the orchestrator-owned backend
// lease between package processes, including shared object-store state. Per-test
// reset paths must use ResetBackendIntegrationState so package-scoped runtime
// fixtures keep their objects until the package boundary.
func ResetBackendIntegrationPackageState(ctx context.Context, stack *BackendIntegrationStack) error {
	if stack == nil || stack.Postgres == nil {
		return fmt.Errorf("backend integration stack is not initialized")
	}
	return errors.Join(
		ResetBackendIntegrationState(ctx, stack),
		resetBackendIntegrationS3State(ctx, stack),
	)
}

func (s *BackendIntegrationStack) Close() error {
	var firstErr error
	for i := len(s.cleanups) - 1; i >= 0; i-- {
		if err := s.cleanups[i].run(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *BackendIntegrationStack) addCleanup(name string, fn func() error) {
	cleanup := newIntegrationCleanup(name, fn)
	s.cleanups = append(s.cleanups, cleanup)

	integrationCleanupMu.Lock()
	integrationRegisteredCleanups = append(integrationRegisteredCleanups, cleanup)
	integrationCleanupMu.Unlock()
}

func runBackendIntegrationKratosMigrate(ctx context.Context, netw *testcontainers.DockerNetwork, dsn string) error {
	ctr, err := testcontainers.Run(ctx, oryKratosImage,
		network.WithNetwork([]string{"kratos-migrate"}, netw),
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
			"SESSION_COOKIE_NAME":                    oryTestSessionCookieName,
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

	ctr, err := testcontainers.Run(ctx, oryKratosImage, kratosOptions...)
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

	adminURL = "http://" + oryHostLoopback + ":" + adminPort.Port()
	publicURL = "http://" + oryHostLoopback + ":" + publicPort.Port()
	if err := backendIntegrationWaitHTTPReady(ctx, adminURL+"/health/ready"); err != nil {
		_ = cleanup()
		return "", "", nil, fmt.Errorf("wait for kratos readiness: %w", err)
	}

	return adminURL, publicURL, cleanup, nil
}

func startBackendIntegrationMinIO(ctx context.Context) (endpoint, mediaBucket, cacheBucket string, cleanup func() error, err error) {
	ctr, err := testcontainers.Run(ctx, runtimeMinIOImage,
		testcontainers.WithExposedPorts("9000/tcp"),
		testcontainers.WithEnv(map[string]string{
			"MINIO_ROOT_USER":     "minioadmin",
			"MINIO_ROOT_PASSWORD": "minioadmin",
		}),
		testcontainers.WithCmd("server", "/data"),
		testcontainers.WithWaitStrategy(
			wait.ForAll(
				wait.ForListeningPort("9000/tcp"),
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
	hostDir := appIntegrationRepoPath(relativeDir)
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
