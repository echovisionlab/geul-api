//go:build integration

package testutil

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	runtimeMinIOImage         = "minio/minio:RELEASE.2025-04-22T22-12-26Z@sha256:a1ea29fa28355559ef137d71fc570e508a214ec84ff8083e39bc5428980b015e"
	runtimeImgproxyImage      = "darthsim/imgproxy:v3.31.0@sha256:6db046632f568931e165d61ce289382804f7bbce5b791db6fb6b8d4ace507378"
	runtimeCDNPort            = "8081/tcp"
	runtimeTokenSigningSecret = IntegrationTokenSigningSecret
	runtimeMediaSigningSecret = "runtime-media-signing-secret"
	runtimeProcessGracePeriod = 2 * time.Second
	runtimeProcessStopTimeout = 15 * time.Second
	runtimeVariantStopTimeout = 30 * time.Second
)

type RuntimeStack struct {
	*OryStack

	S3Region           string
	S3Endpoint         string
	S3AccessKeyID      string
	S3SecretAccessKey  string
	S3ForcePathStyle   bool
	S3MediaBucket      string
	S3CacheBucket      string
	TokenSigningSecret string
	MediaSigningSecret string

	BackendURL    string
	CollabURL     string
	CollabHealth  string
	WebURL        string
	CDNURL        string
	MediaURL      string
	ImgproxyURL   string
	TranscoderURL string
	WaveformURL   string

	BackendReadTimeoutSec   int
	BackendWriteTimeoutSec  int
	BackendIdleTimeoutSec   int
	EditorImageMaxSizeBytes int64

	backendS3Endpoint      string
	s3CompleteFailureProxy *runtimeS3CompleteFailureProxy
	backendProc            *runtimeProcess
	collabProc             *runtimeProcess
	cdnContainer           testcontainers.Container
	imgproxyContainer      testcontainers.Container
	imgproxyStarted        bool
	transcoderProc         *runtimeProcess
	waveformProc           *runtimeProcess
	waveformFFmpegPath     string
}

type runtimeProcess struct {
	name      string
	pid       int
	logPath   string
	done      chan struct{}
	waitMu    sync.Mutex
	waitErr   error
	cancel    context.CancelFunc
	healthURL string

	watchdogCancel context.CancelFunc
	watchdogDone   <-chan struct{}
	stopOnce       sync.Once
	stopErr        error
}

type runtimeApplicationCoordinator struct {
	mu     sync.Mutex
	active *RuntimeStack
}

var (
	runtimeSharedStackOnce sync.Once
	runtimeSharedStack     *RuntimeStack
	runtimeSharedCDNOnce   sync.Once

	runtimeSharedCompleteMultipartFailureStackOnce sync.Once
	runtimeSharedCompleteMultipartFailureStack     *RuntimeStack

	runtimeSharedDirectMediaStackOnce sync.Once
	runtimeSharedDirectMediaStack     *RuntimeStack
	runtimeSharedDirectMediaCDNOnce   sync.Once
	runtimeSharedDirectMediaCDNStack  *RuntimeStack

	runtimeSharedWaveformFailureStackOnce sync.Once
	runtimeSharedWaveformFailureStack     *RuntimeStack
	runtimeApplicationProcesses           runtimeApplicationCoordinator

	runtimeReservedPortMu sync.Mutex
	runtimeReservedPorts  = map[int]struct{}{}
)

func SetupRuntimeStack(t *testing.T) *RuntimeStack {
	t.Helper()

	backendPort := reserveLocalPort(t)
	webPort := reserveLocalPort(t)
	cdnPort := reserveLocalPort(t)
	backendURL := fmt.Sprintf("http://127.0.0.1:%d", backendPort)
	webURL := fmt.Sprintf("http://127.0.0.1:%d", webPort)
	cdnURL := fmt.Sprintf("http://127.0.0.1:%d", cdnPort)
	hookURL, err := rewriteHostURLForContainer(backendURL)
	require.NoError(t, err)

	return setupSuiteRuntimeStack(t, backendURL, webURL, cdnURL, hookURL)
}

func setupSuiteRuntimeStack(t *testing.T, backendURL, webURL, cdnURL, hookURL string) *RuntimeStack {
	t.Helper()

	lease, err := loadAppIntegrationLease(currentAppIntegrationLeasePath())
	require.NoError(t, err)
	backend, err := runtimeBackendLease(lease)
	require.NoError(t, err)
	ory := SetupOryStackWithOptions(t, OryStackOptions{
		BrowserBaseURL: webURL,
		HookBaseURL:    hookURL,
		IgnoreExternal: true,
	})

	stack := newRuntimeStackFromBackendLease(ory, backend, backendURL, webURL, cdnURL)
	stack.CollabURL = fmt.Sprintf("http://127.0.0.1:%d", reserveLocalPort(t))
	stack.CollabHealth = fmt.Sprintf("http://127.0.0.1:%d/health", reserveLocalPort(t))
	return stack
}

func runtimeBackendLease(lease AppIntegrationLeaseDescriptor) (*AppIntegrationBackendLease, error) {
	if lease.Backend == nil {
		return nil, fmt.Errorf("orchestrator-owned runtime integration stack is required")
	}
	backend := lease.Backend
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "S3 region", value: backend.S3Region},
		{name: "S3 endpoint", value: backend.S3Endpoint},
		{name: "S3 access key ID", value: backend.S3AccessKeyID},
		{name: "S3 secret access key", value: backend.S3SecretAccessKey},
		{name: "S3 media bucket", value: backend.S3MediaBucket},
		{name: "S3 cache bucket", value: backend.S3CacheBucket},
	} {
		if strings.TrimSpace(field.value) == "" {
			return nil, fmt.Errorf("orchestrator runtime backend lease %s is required", field.name)
		}
	}
	return backend, nil
}

func newRuntimeStackFromBackendLease(
	ory *OryStack,
	backend *AppIntegrationBackendLease,
	backendURL string,
	webURL string,
	cdnURL string,
) *RuntimeStack {
	return &RuntimeStack{
		OryStack:               ory,
		S3Region:               backend.S3Region,
		S3Endpoint:             backend.S3Endpoint,
		S3AccessKeyID:          backend.S3AccessKeyID,
		S3SecretAccessKey:      backend.S3SecretAccessKey,
		S3ForcePathStyle:       backend.S3ForcePathStyle,
		S3MediaBucket:          backend.S3MediaBucket,
		S3CacheBucket:          backend.S3CacheBucket,
		TokenSigningSecret:     runtimeTokenSigningSecret,
		MediaSigningSecret:     runtimeMediaSigningSecret,
		BackendURL:             backendURL,
		WebURL:                 webURL,
		CDNURL:                 cdnURL,
		MediaURL:               cdnURL,
		BackendReadTimeoutSec:  10,
		BackendWriteTimeoutSec: 30,
		BackendIdleTimeoutSec:  60,
	}
}

// SetupSharedRuntimeStack starts the default non-web runtime stack once per
// integration suite. The short backend read timeout is intentional for upload
// interruption cases that assert aborted or interrupted part uploads.
func SetupSharedRuntimeStack(t *testing.T) *RuntimeStack {
	t.Helper()

	runtimeSharedStackOnce.Do(func() {
		withIntegrationSuiteCleanupRegistration(func() {
			stack := SetupRuntimeStack(t)
			stack.BackendReadTimeoutSec = 1
			runtimeSharedStack = stack
		})
	})

	stack := requireSharedRuntimeStack(t, runtimeSharedStack)
	activateRuntimeApplicationStack(t, stack, func() {
		stack.StartAllNonWebProcesses(t)
	})
	activateRuntimeHook(t, stack)
	return stack
}

func SetupSharedRuntimeStackWithCDN(t *testing.T) *RuntimeStack {
	t.Helper()

	stack := SetupSharedRuntimeStack(t)
	runtimeSharedCDNOnce.Do(func() {
		withIntegrationSuiteCleanupRegistration(func() {
			stack.StartCDN(t)
		})
	})
	stack = requireSharedRuntimeStack(t, stack)
	activateRuntimeHook(t, stack)
	return stack
}

// SetupSharedRuntimeCompleteMultipartFailureStack starts a browser-free runtime
// stack whose backend S3 endpoint transparently proxies MinIO. Tests explicitly
// mark upload IDs whose CompleteMultipartUpload request must fail.
func SetupSharedRuntimeCompleteMultipartFailureStack(t *testing.T) *RuntimeStack {
	t.Helper()

	runtimeSharedCompleteMultipartFailureStackOnce.Do(func() {
		withIntegrationSuiteCleanupRegistration(func() {
			stack := SetupRuntimeStack(t)
			failureProxy := newRuntimeS3CompleteFailureProxy(t, stack.S3Endpoint)
			stack.backendS3Endpoint = failureProxy.server.URL
			stack.s3CompleteFailureProxy = failureProxy
			runtimeSharedCompleteMultipartFailureStack = stack
		})
	})

	stack := requireSharedRuntimeStack(t, runtimeSharedCompleteMultipartFailureStack)
	activateRuntimeApplicationStack(t, stack, func() {
		stack.StartAllNonWebProcesses(t)
	})
	activateRuntimeHook(t, stack)
	return stack
}

// SetupSharedDirectMediaRuntimeStack starts the shared dependencies needed by
// direct media service tests without launching backend, collab, transcoder, or
// waveform processes.
func SetupSharedDirectMediaRuntimeStack(t *testing.T) *RuntimeStack {
	t.Helper()

	runtimeSharedDirectMediaStackOnce.Do(func() {
		withIntegrationSuiteCleanupRegistration(func() {
			stack := SetupDirectMediaRuntimeStack(t)
			runtimeSharedDirectMediaStack = stack
		})
	})

	stack := requireSharedRuntimeStack(t, runtimeSharedDirectMediaStack)
	activateRuntimeApplicationStack(t, stack, nil)
	activateRuntimeHook(t, stack)
	return stack
}

func SetupSharedDirectMediaRuntimeStackWithCDN(t *testing.T) *RuntimeStack {
	t.Helper()

	runtimeSharedDirectMediaCDNOnce.Do(func() {
		withIntegrationSuiteCleanupRegistration(func() {
			runtimeSharedDirectMediaCDNStack = SetupDirectMediaRuntimeStack(t)
		})
	})
	stack := requireSharedRuntimeStack(t, runtimeSharedDirectMediaCDNStack)
	activateRuntimeApplicationStack(t, stack, nil)
	if stack.cdnContainer == nil {
		withIntegrationSuiteCleanupRegistration(func() {
			stack.StartCDN(t)
		})
	}
	activateRuntimeHook(t, stack)
	return stack
}

func SetupDirectMediaRuntimeStack(t *testing.T) *RuntimeStack {
	t.Helper()

	backendPort := reserveLocalPort(t)
	webPort := reserveLocalPort(t)
	cdnPort := reserveLocalPort(t)
	backendURL := fmt.Sprintf("http://127.0.0.1:%d", backendPort)
	webURL := fmt.Sprintf("http://127.0.0.1:%d", webPort)
	cdnURL := fmt.Sprintf("http://127.0.0.1:%d", cdnPort)
	hookURL, err := rewriteHostURLForContainer(backendURL)
	require.NoError(t, err)

	return setupSuiteRuntimeStack(t, backendURL, webURL, cdnURL, hookURL)
}

// SetupSharedRuntimeWaveformFailureStack starts the browser-free runtime with
// a waveform worker whose FFmpeg executable always fails.
func SetupSharedRuntimeWaveformFailureStack(t *testing.T) *RuntimeStack {
	t.Helper()

	runtimeSharedWaveformFailureStackOnce.Do(func() {
		withIntegrationSuiteCleanupRegistration(func() {
			stack := SetupRuntimeStack(t)
			stack.waveformFFmpegPath = GenerateFailingFFmpegExecutable(
				t,
				integrationTempDir(t, "waveform-failure-ffmpeg"),
			)
			runtimeSharedWaveformFailureStack = stack
		})
	})

	stack := requireSharedRuntimeStack(t, runtimeSharedWaveformFailureStack)
	activateRuntimeApplicationStack(t, stack, func() {
		stack.StartBackend(t)
		stack.StartCollab(t)
		stack.StartTranscoder(t)
		stack.StartWaveformProcessorWithFFmpeg(t, stack.waveformFFmpegPath)
	})
	activateRuntimeHook(t, stack)
	return stack
}

func activateRuntimeApplicationStack(t *testing.T, stack *RuntimeStack, start func()) {
	t.Helper()
	require.NotNil(t, stack)

	ctx, cancel := context.WithTimeout(t.Context(), runtimeVariantStopTimeout)
	defer cancel()
	err := runtimeApplicationProcesses.activate(ctx, stack, func() {
		if start == nil {
			return
		}
		withIntegrationSuiteCleanupRegistration(start)
	})
	require.NoError(t, err)
}

func (coordinator *runtimeApplicationCoordinator) activate(
	ctx context.Context,
	stack *RuntimeStack,
	start func(),
) error {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()

	if coordinator.active != nil && coordinator.active != stack {
		if err := coordinator.active.stopApplicationProcesses(ctx); err != nil {
			coordinator.active = nil
			return fmt.Errorf("stop previous runtime application stack: %w", err)
		}
	}
	coordinator.active = stack
	if start != nil {
		start()
	}
	return nil
}

func (stack *RuntimeStack) stopApplicationProcesses(ctx context.Context) error {
	processes := []*runtimeProcess{
		stack.collabProc,
		stack.transcoderProc,
		stack.waveformProc,
		stack.backendProc,
	}
	stack.collabProc = nil
	stack.transcoderProc = nil
	stack.waveformProc = nil
	stack.backendProc = nil

	for _, process := range processes {
		if process != nil {
			process.cancel()
		}
	}
	var stopErr error
	for _, process := range processes {
		if process == nil {
			continue
		}
		if err := process.stop(ctx); err != nil {
			stopErr = errors.Join(stopErr, err)
		}
	}
	return stopErr
}

func activateRuntimeHook(t *testing.T, stack *RuntimeStack) {
	t.Helper()
	require.NotNil(t, stack)
	lease, err := loadAppIntegrationLease(currentAppIntegrationLeasePath())
	require.NoError(t, err)
	backend, err := runtimeBackendLease(lease)
	require.NoError(t, err)
	unregister, err := registerRuntimeHook(t.Context(), backend, stack.BackendURL)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := unregister(cleanupCtx); err != nil {
			t.Errorf("deactivate runtime hook: %v", err)
		}
	})
}

func registerRuntimeHook(
	ctx context.Context,
	backend *AppIntegrationBackendLease,
	rawBackendURL string,
) (func(context.Context) error, error) {
	if backend == nil {
		return nil, fmt.Errorf("orchestrator-owned runtime integration stack is required")
	}
	hookURL, err := rewriteHostURLForContainer(rawBackendURL)
	if err != nil {
		return nil, fmt.Errorf("resolve runtime hook upstream: %w", err)
	}
	return registerSuiteHookUpstream(ctx, backend, hookURL)
}

func requireSharedRuntimeStack(t *testing.T, stack *RuntimeStack) *RuntimeStack {
	t.Helper()

	require.NotNil(t, stack)
	t.Cleanup(func() {
		if t.Failed() {
			stack.DumpProcessLogs(t)
		}
	})
	return stack
}

func (s *RuntimeStack) WaitForCollabFileIngestProjectionApplied(
	t *testing.T,
	fileID string,
) {
	t.Helper()
	require.NotNil(t, s.collabProc)

	require.Eventually(t, func() bool {
		data, err := os.ReadFile(s.collabProc.logPath)
		if err != nil {
			return false
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, `"event":"file.ingest.projection_applied"`) &&
				strings.Contains(line, `"file_id":"`+fileID+`"`) {
				return true
			}
		}
		return false
	}, 20*time.Second, 100*time.Millisecond, "collab did not complete the applied file ingest projection")
}

func (s *RuntimeStack) CreateUser(t *testing.T, role string) *OryUser {
	t.Helper()
	return s.OryStack.CreateUser(t, role)
}

func (s *RuntimeStack) StartBackend(t *testing.T) {
	t.Helper()
	if s.backendProc != nil {
		return
	}

	if s.BackendURL == "" {
		s.BackendURL = fmt.Sprintf("http://127.0.0.1:%d", reserveLocalPort(t))
	}
	parsed, err := urlFromString(s.BackendURL)
	require.NoError(t, err)
	port := parsed.Port()
	require.NotEmpty(t, port)
	tempDir := integrationTempDir(t, "backend")
	corsOrigin := s.WebURL
	if corsOrigin == "" {
		corsOrigin = "http://127.0.0.1:3000"
	}
	cdnURL := s.CDNURL
	if cdnURL == "" {
		cdnURL = "http://127.0.0.1:9999"
	}
	mediaURL := s.MediaURL
	if mediaURL == "" {
		mediaURL = cdnURL
	}
	siteOrigin := s.WebURL
	if siteOrigin == "" {
		siteOrigin = "http://127.0.0.1:3000"
	}
	s3Endpoint := s.S3Endpoint
	if s.backendS3Endpoint != "" {
		s3Endpoint = s.backendS3Endpoint
	}
	backendEnv := map[string]string{
		"PORT":                         port,
		"AUTH_HEADER_NAME":             "X-Authenticated-Context-B64",
		"INTERNAL_SERVICE_HEADER_NAME": "X-Internal-Service",
		"DATABASE_DSN":                 s.PostgresDSN,
		"CORS_ORIGINS":                 corsOrigin,
		"S3_MEDIA_BUCKET":              s.S3MediaBucket,
		"S3_CACHE_BUCKET":              s.S3CacheBucket,
		"S3_REGION":                    s.S3Region,
		"S3_ENDPOINT":                  s3Endpoint,
		"S3_PUBLIC_ENDPOINT":           s.S3Endpoint,
		"S3_ACCESS_KEY_ID":             s.S3AccessKeyID,
		"S3_SECRET_ACCESS_KEY":         s.S3SecretAccessKey,
		"S3_FORCE_PATH_STYLE":          "true",
		"CDN_URL":                      cdnURL,
		"MEDIA_URL":                    mediaURL,
		"KRATOS_URL":                   s.KratosPublicURL,
		"KRATOS_ADMIN_URL":             s.KratosAdminURL,
		"SPICEDB_ENDPOINT":             s.SpiceDBEndpoint,
		"SPICEDB_GRPC_PRESHARED_KEY":   s.SpiceDBToken,
		"SPICEDB_ALLOW_INSECURE":       "true",
		"SITE_ORIGIN":                  siteOrigin,
		"EDITOR_COLLAB_URL":            s.CollabURL,
		"GOOGLE_AI_API_KEY":            "integration-dummy",
		"MAXMIND_ACCOUNT_ID":           "integration",
		"MAXMIND_LICENSE_KEY":          "integration",
		"TOKEN_SIGNING_SECRET":         s.TokenSigningSecret,
		"MEDIA_SIGNING_SECRET":         s.MediaSigningSecret,
		"CLOUDFLARE_ZONE_ID":           "integration-zone",
		"CLOUDFLARE_API_TOKEN":         "integration-cloudflare-token",
		"CLOUDFLARE_API_URL":           "http://127.0.0.1:9",
		"INSTANCE_ID":                  "integration-backend",
		"LOG_LEVEL":                    "debug",
		"TMPDIR":                       tempDir,
		"EDITOR_IMAGE_MAX_SIZE_BYTES":  fmt.Sprintf("%d", s.EditorImageMaxSizeBytes),
		"HTTP_READ_TIMEOUT_SEC":        fmt.Sprintf("%d", s.BackendReadTimeoutSec),
		"HTTP_WRITE_TIMEOUT_SEC":       fmt.Sprintf("%d", s.BackendWriteTimeoutSec),
		"HTTP_IDLE_TIMEOUT_SEC":        fmt.Sprintf("%d", s.BackendIdleTimeoutSec),
	}
	s.backendProc = startRuntimeProcess(t, runtimeProcessSpec{
		Name:      "backend",
		Workdir:   appIntegrationRepoPath("../.."),
		Command:   "go",
		Args:      []string{"run", "./cmd/server"},
		HealthURL: s.BackendURL + "/health",
		Env:       backendEnv,
	})
}

func (s *RuntimeStack) StartCollab(t *testing.T) {
	t.Helper()
	if s.collabProc != nil {
		return
	}
	s.StartBackend(t)

	collabURL, err := url.Parse(s.CollabURL)
	require.NoError(t, err)
	healthURL, err := url.Parse(s.CollabHealth)
	require.NoError(t, err)
	port := collabURL.Port()
	healthPort := healthURL.Port()
	require.NotEmpty(t, port)
	require.NotEmpty(t, healthPort)
	collabEnv := map[string]string{
		"PORT":                  port,
		"HEALTH_PORT":           healthPort,
		"DATABASE_DSN":          s.PostgresDSN,
		"API_URL":               s.BackendURL,
		"CDN_URL":               s.CDNURL,
		"MANAGED_MEDIA_ORIGINS": s.CDNURL,
		"DEBUG":                 "1",
	}
	for key, value := range runtimeCollabSourceEnv() {
		collabEnv[key] = value
	}
	for key, value := range runtimeCollabTrustEnv(s.TokenSigningSecret) {
		collabEnv[key] = value
	}
	s.collabProc = startRuntimeProcess(t, runtimeProcessSpec{
		Name:      "collab",
		Workdir:   appIntegrationRepoPath("../../../../apps/collab"),
		Command:   "pnpm",
		Args:      []string{"exec", "tsx", "index.ts"},
		HealthURL: s.CollabHealth,
		Env:       collabEnv,
	})
}

func runtimeCollabTrustEnv(tokenSigningSecret string) map[string]string {
	return map[string]string{
		"TOKEN_SIGNING_SECRET": tokenSigningSecret,
	}
}

func runtimeCollabSourceEnv() map[string]string {
	// Runtime integration executes the current editor-collab source, so it must
	// resolve the current dirty Contract/Common trees instead of released pins.
	// Production start/build continues to use the package.json release pins.
	return map[string]string{
		"TSX_TSCONFIG_PATH": "tsconfig.local-dependencies.json",
	}
}

func runtimeOathkeeperTrustEnv(tokenSigningSecret string) map[string]string {
	return map[string]string{
		"TOKEN_SIGNING_SECRET": tokenSigningSecret,
	}
}

func (s *RuntimeStack) StartImgproxy(t *testing.T) {
	t.Helper()
	if s.imgproxyStarted {
		return
	}

	ctx := context.Background()
	s3EndpointForContainer, err := rewriteHostURLForContainer(s.S3Endpoint)
	require.NoError(t, err)
	hostAccessOptions, err := hostAccessOptionsForURL(s3EndpointForContainer)
	require.NoError(t, err)

	imgproxyOptions := []testcontainers.ContainerCustomizer{
		testcontainers.WithExposedPorts("8080/tcp"),
		testcontainers.WithEnv(map[string]string{
			"IMGPROXY_KEY":                 "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"IMGPROXY_SALT":                "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
			"IMGPROXY_USE_S3":              "true",
			"IMGPROXY_S3_ENDPOINT":         s3EndpointForContainer,
			"AWS_ACCESS_KEY_ID":            s.S3AccessKeyID,
			"AWS_SECRET_ACCESS_KEY":        s.S3SecretAccessKey,
			"AWS_REGION":                   s.S3Region,
			"IMGPROXY_ALLOW_INSECURE_URLS": "false",
			"IMGPROXY_WORKERS":             "2",
			"IMGPROXY_PREFERRED_FORMATS":   "webp",
			"IMGPROXY_ENFORCE_AVIF":        "false",
			"IMGPROXY_TTL":                 "31536000",
		}),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("8080/tcp").WithStartupTimeout(2 * time.Minute)),
	}
	imgproxyOptions = append(imgproxyOptions, hostAccessOptions...)

	container, err := testcontainers.Run(ctx, runtimeImgproxyImage, imgproxyOptions...)
	require.NoError(t, err)
	registerIntegrationCleanup(t, "imgproxy container", func() error {
		return runBackendIntegrationBoundedCleanup(func(ctx context.Context) error {
			return container.Terminate(ctx)
		})
	})

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "8080/tcp")
	require.NoError(t, err)

	s.ImgproxyURL = fmt.Sprintf("http://%s:%s", host, port.Port())
	s.imgproxyContainer = container
	s.imgproxyStarted = true
}

func (s *RuntimeStack) StartCDN(t *testing.T) {
	t.Helper()
	if s.cdnContainer != nil {
		return
	}
	backend, err := CurrentAppIntegrationBackendLease()
	require.NoError(t, err)
	cdnImage := strings.TrimSpace(backend.CDNImage)
	require.NotEmpty(t, cdnImage, "suite backend lease must select an already-local canonical CDN image")

	s.StartImgproxy(t)

	if s.CDNURL == "" {
		s.CDNURL = fmt.Sprintf("http://127.0.0.1:%d", reserveLocalPort(t))
	}
	parsed, err := urlFromString(s.CDNURL)
	require.NoError(t, err)
	port := parsed.Port()
	require.NotEmpty(t, port)

	ctx := context.Background()
	s3EndpointForContainer, err := rewriteHostURLForContainer(s.S3Endpoint)
	require.NoError(t, err)
	imgproxyURLForContainer, err := rewriteHostURLForContainer(s.ImgproxyURL)
	require.NoError(t, err)

	hostAccessOptions, err := hostAccessOptionsForURL(s3EndpointForContainer)
	require.NoError(t, err)
	imgproxyHostAccessOptions, err := hostAccessOptionsForURL(imgproxyURLForContainer)
	require.NoError(t, err)
	hostAccessOptions = append(hostAccessOptions, imgproxyHostAccessOptions...)

	cdnPort := network.MustParsePort(runtimeCDNPort)
	cdnOptions := []testcontainers.ContainerCustomizer{
		testcontainers.WithExposedPorts(runtimeCDNPort),
		testcontainers.WithHostConfigModifier(func(hostConfig *container.HostConfig) {
			if hostConfig.PortBindings == nil {
				hostConfig.PortBindings = network.PortMap{}
			}
			hostConfig.PortBindings[cdnPort] = []network.PortBinding{{
				HostIP:   netip.MustParseAddr("127.0.0.1"),
				HostPort: port,
			}}
		}),
		testcontainers.WithEnv(map[string]string{
			"CDN_PORT":                     strings.TrimSuffix(runtimeCDNPort, "/tcp"),
			"S3_ENDPOINT":                  s3EndpointForContainer,
			"S3_REGION":                    s.S3Region,
			"S3_ACCESS_KEY_ID":             s.S3AccessKeyID,
			"S3_SECRET_ACCESS_KEY":         s.S3SecretAccessKey,
			"S3_CACHE_BUCKET":              s.S3CacheBucket,
			"S3_MEDIA_BUCKET":              s.S3MediaBucket,
			"S3_RELEASE_BUCKET":            s.S3MediaBucket,
			"S3_RELEASE_ACCESS_KEY_ID":     s.S3AccessKeyID,
			"S3_RELEASE_SECRET_ACCESS_KEY": s.S3SecretAccessKey,
			"S3_FORCE_PATH_STYLE":          "true",
			"CDN_FONT_S3_PREFIX":           "fonts/",
			"CDN_FONT_UPSTREAM_URL":        "https://fonts.gstatic.com",
			"CDN_FONT_CSS_UPSTREAM":        "https://fonts.googleapis.com",
			"CDN_FONT_CACHE_MAX_AGE":       "31536000",
			"CDN_PUBLIC_URL":               s.CDNURL,
			"CDN_IMGPROXY_URL":             imgproxyURLForContainer,
			"IMGPROXY_KEY":                 "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"IMGPROXY_SALT":                "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
			"CDN_IMAGE_CACHE_MAX_AGE":      "31536000",
			"CDN_MEDIA_CACHE_MAX_AGE":      "86400",
			"MEDIA_SIGNING_SECRET":         s.MediaSigningSecret,
			"CDN_ALLOWED_ORIGINS":          s.WebURL,
			"OTEL_SDK_DISABLED":            "true",
		}),
		testcontainers.WithWaitStrategy(
			wait.ForHTTP("/health").WithPort(runtimeCDNPort).WithStartupTimeout(2 * time.Minute),
		),
	}
	cdnOptions = append(cdnOptions, hostAccessOptions...)

	container, err := testcontainers.Run(ctx, cdnImage, cdnOptions...)
	require.NoError(t, err)
	registerIntegrationCleanup(t, "cdn container", func() error {
		return runBackendIntegrationBoundedCleanup(func(ctx context.Context) error {
			return container.Terminate(ctx)
		})
	})
	s.cdnContainer = container
	s.warmCDNImageProxy(t)
}

func (s *RuntimeStack) warmCDNImageProxy(t *testing.T) {
	t.Helper()

	imageBody, err := os.ReadFile(RepositoryTestImageJPEG(t))
	require.NoError(t, err)
	require.NotEmpty(t, imageBody)

	assetID := uuid.NewString()
	key, err := mediaauth.AssetObjectKey(assetID, "jpg")
	require.NoError(t, err)
	client := s.runtimeS3Client(t)
	_, err = client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket:      aws.String(s.S3MediaBucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(imageBody),
		ContentType: aws.String("image/jpeg"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteObject(context.Background(), &s3.DeleteObjectInput{
			Bucket: aws.String(s.S3MediaBucket),
			Key:    aws.String(key),
		})
	})

	assetPath, err := mediaauth.AssetPath(assetID, "image", "jpg")
	require.NoError(t, err)
	warmURL := strings.TrimRight(s.CDNURL, "/") + assetPath + "?w=64&h=64&q=80"
	httpClient := &http.Client{Timeout: 5 * time.Second}
	var lastErr error
	for attempt := 0; attempt < 10; attempt++ {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, warmURL, nil)
		require.NoError(t, err)
		req.Header.Set("Accept", "image/webp,image/*;q=0.8,*/*;q=0.1")

		resp, err := httpClient.Do(req)
		if err != nil {
			lastErr = err
		} else {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				if len(body) > 0 {
					return
				}
				lastErr = fmt.Errorf("status %d with empty body", resp.StatusCode)
			} else {
				lastErr = fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
			}
		}
		if attempt < 9 {
			time.Sleep(200 * time.Millisecond)
		}
	}
	if lastErr != nil {
		s.DumpProcessLogs(t)
	}
	require.NoError(t, lastErr, "CDN image proxy warmup failed")
}

func (s *RuntimeStack) runtimeS3Client(t *testing.T) *s3.Client {
	t.Helper()

	awsCfg, err := awsconfig.LoadDefaultConfig(
		context.Background(),
		awsconfig.WithRegion(s.S3Region),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(s.S3AccessKeyID, s.S3SecretAccessKey, ""),
		),
	)
	require.NoError(t, err)

	return s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(s.S3Endpoint)
		o.UsePathStyle = s.S3ForcePathStyle
	})
}

func (s *RuntimeStack) StartTranscoder(t *testing.T) {
	t.Helper()
	if s.transcoderProc != nil {
		return
	}

	port := reserveLocalPort(t)
	s.TranscoderURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	tempDir := filepath.Join(integrationTempDir(t, "transcoder"), "transcoder")
	require.NoError(t, os.MkdirAll(tempDir, 0o755))
	s.transcoderProc = startRuntimeProcess(t, runtimeProcessSpec{
		Name:      "transcoder",
		Workdir:   appIntegrationRepoPath("../../../../apps/transcoder"),
		Command:   "go",
		Args:      []string{"run", "./cmd/transcoder"},
		HealthURL: s.TranscoderURL + "/health",
		Env: map[string]string{
			"PORT":                    fmt.Sprintf("%d", port),
			"S3_MEDIA_BUCKET":         s.S3MediaBucket,
			"S3_REGION":               s.S3Region,
			"S3_ENDPOINT":             s.S3Endpoint,
			"S3_ACCESS_KEY_ID":        s.S3AccessKeyID,
			"S3_SECRET_ACCESS_KEY":    s.S3SecretAccessKey,
			"S3_FORCE_PATH_STYLE":     "true",
			"DATABASE_DSN":            s.PostgresDSN,
			"FFMPEG_PATH":             "ffmpeg",
			"FFPROBE_PATH":            "ffprobe",
			"FFMPEG_TEMP_DIR":         tempDir,
			"WORKER_COUNT":            "1",
			"JOB_TIMEOUT_MINUTES":     "10",
			"AUDIO_HLS_BITRATE":       "128k",
			"MAX_RETRIES":             "2",
			"PREFETCH_COUNT":          "1",
			"WAVEFORM_WORKER_COUNT":   "1",
			"WAVEFORM_PREFETCH_COUNT": "1",
			"LOG_LEVEL":               "info",
			"INSTANCE_ID":             "integration-transcoder",
		},
	})
}

func (s *RuntimeStack) StartWaveformProcessor(t *testing.T) {
	s.StartWaveformProcessorWithFFmpeg(t, "ffmpeg")
}

func (s *RuntimeStack) StartWaveformProcessorWithFFmpeg(t *testing.T, ffmpegPath string) {
	t.Helper()
	if s.waveformProc != nil {
		return
	}

	port := reserveLocalPort(t)
	s.WaveformURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	tempDir := filepath.Join(integrationTempDir(t, "waveform"), "waveform")
	require.NoError(t, os.MkdirAll(tempDir, 0o755))
	s.waveformProc = startRuntimeProcess(t, runtimeProcessSpec{
		Name:      "waveform-processor",
		Workdir:   appIntegrationRepoPath("../../../../apps/transcoder"),
		Command:   "go",
		Args:      []string{"run", "./cmd/waveform-processor"},
		HealthURL: s.WaveformURL + "/health",
		Env: map[string]string{
			"PORT":                    fmt.Sprintf("%d", port),
			"S3_MEDIA_BUCKET":         s.S3MediaBucket,
			"S3_REGION":               s.S3Region,
			"S3_ENDPOINT":             s.S3Endpoint,
			"S3_ACCESS_KEY_ID":        s.S3AccessKeyID,
			"S3_SECRET_ACCESS_KEY":    s.S3SecretAccessKey,
			"S3_FORCE_PATH_STYLE":     "true",
			"DATABASE_DSN":            s.PostgresDSN,
			"FFMPEG_PATH":             ffmpegPath,
			"FFPROBE_PATH":            "ffprobe",
			"FFMPEG_TEMP_DIR":         tempDir,
			"WORKER_COUNT":            "1",
			"JOB_TIMEOUT_MINUTES":     "10",
			"AUDIO_HLS_BITRATE":       "128k",
			"MAX_RETRIES":             "2",
			"PREFETCH_COUNT":          "1",
			"WAVEFORM_WORKER_COUNT":   "1",
			"WAVEFORM_PREFETCH_COUNT": "1",
			"LOG_LEVEL":               "info",
			"INSTANCE_ID":             "integration-waveform",
		},
	})
}

func (s *RuntimeStack) StartAllNonWebProcesses(t *testing.T) {
	t.Helper()
	s.StartBackend(t)
	s.StartCollab(t)
	s.StartTranscoder(t)
	s.StartWaveformProcessor(t)
}

type runtimeProcessSpec struct {
	Name      string
	Workdir   string
	Command   string
	Args      []string
	Env       map[string]string
	HealthURL string
}

func urlFromString(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid url: %s", value)
	}
	return parsed, nil
}

func rewriteHostURLForContainer(value string) (string, error) {
	parsed, err := urlFromString(value)
	if err != nil {
		return "", err
	}

	host := parsed.Hostname()
	if host == "127.0.0.1" || host == "localhost" || host == "0.0.0.0" {
		parsed.Host = net.JoinHostPort(testcontainers.HostInternal, parsed.Port())
	}
	return parsed.String(), nil
}

func startRuntimeProcess(t *testing.T, spec runtimeProcessSpec) *runtimeProcess {
	t.Helper()

	logDir := integrationTempDir(t, spec.Name+"-log")
	logPath := filepath.Join(logDir, spec.Name+".log")
	logFile, err := os.Create(logPath)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cmdArgs := append([]string{spec.Command}, spec.Args...)
	cmd := exec.CommandContext(ctx, "/usr/bin/env", cmdArgs...)
	cmd.Dir = spec.Workdir
	cmd.Env = append(os.Environ(), formatRuntimeEnv(spec.Env)...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	cmd.WaitDelay = 10 * time.Second

	require.NoError(t, cmd.Start())
	watchdogCancel, watchdogDone, watchdogErr := startRuntimeParentWatchdog(os.Getpid(), cmd.Process.Pid)
	if watchdogErr != nil {
		cancel()
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		_ = logFile.Close()
		require.NoError(t, watchdogErr)
	}

	process := &runtimeProcess{
		name:      spec.Name,
		pid:       cmd.Process.Pid,
		logPath:   logPath,
		done:      make(chan struct{}),
		cancel:    cancel,
		healthURL: spec.HealthURL,

		watchdogCancel: watchdogCancel,
		watchdogDone:   watchdogDone,
	}

	go func() {
		err := cmd.Wait()
		_ = logFile.Close()
		process.setWaitErr(err)
		close(process.done)
	}()
	registerIntegrationCleanup(t, spec.Name+" process", func() error {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), runtimeProcessStopTimeout)
		defer stopCancel()
		return process.stop(stopCtx)
	})

	waitForRuntimeHealth(t, process)

	return process
}

const runtimeParentWatchdogScript = `
parent_pid=$1
target_pgid=$2
while kill -0 "$parent_pid" 2>/dev/null && kill -0 -- "-$target_pgid" 2>/dev/null; do
  sleep 0.2
done
if ! kill -0 "$parent_pid" 2>/dev/null; then
  kill -TERM -- "-$target_pgid" 2>/dev/null || true
  sleep 2
  kill -KILL -- "-$target_pgid" 2>/dev/null || true
fi
`

func startRuntimeParentWatchdog(parentPID, targetPGID int) (context.CancelFunc, <-chan struct{}, error) {
	ctx, cancel := context.WithCancel(context.Background())
	command := exec.CommandContext(
		ctx,
		"/bin/sh",
		"-c",
		runtimeParentWatchdogScript,
		"runtime-process-watchdog",
		strconv.Itoa(parentPID),
		strconv.Itoa(targetPGID),
	)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		cancel()
		return nil, nil, fmt.Errorf("start runtime parent watchdog: %w", err)
	}
	done := make(chan struct{})
	go func() {
		_ = command.Wait()
		close(done)
	}()
	return cancel, done, nil
}

func (process *runtimeProcess) stop(ctx context.Context) error {
	process.stopOnce.Do(func() {
		if process.pid <= 0 {
			process.stopErr = fmt.Errorf("%s process group ID is invalid", process.name)
			return
		}
		process.cancel()
		if err := signalRuntimeProcessGroup(process.pid, syscall.SIGTERM); err != nil {
			process.stopErr = errors.Join(process.stopErr, fmt.Errorf("terminate %s process group: %w", process.name, err))
		}

		graceTimer := time.NewTimer(runtimeProcessGracePeriod)
		defer graceTimer.Stop()
		poll := time.NewTicker(50 * time.Millisecond)
		defer poll.Stop()
		forceSent := false
		groupStopped := false
		for !groupStopped {
			alive, err := runtimeProcessGroupAlive(process.pid)
			if err != nil {
				process.stopErr = errors.Join(process.stopErr, fmt.Errorf("inspect %s process group: %w", process.name, err))
				break
			}
			if !alive {
				groupStopped = true
				break
			}
			select {
			case <-ctx.Done():
				if err := signalRuntimeProcessGroup(process.pid, syscall.SIGKILL); err != nil {
					process.stopErr = errors.Join(process.stopErr, fmt.Errorf("kill %s process group: %w", process.name, err))
				}
				process.stopErr = errors.Join(
					process.stopErr,
					fmt.Errorf("timed out waiting for %s process group to stop: %w", process.name, ctx.Err()),
				)
				groupStopped = true
			case <-graceTimer.C:
				if !forceSent {
					if err := signalRuntimeProcessGroup(process.pid, syscall.SIGKILL); err != nil {
						process.stopErr = errors.Join(process.stopErr, fmt.Errorf("kill %s process group: %w", process.name, err))
					}
					forceSent = true
				}
			case <-poll.C:
			}
		}

		if groupStopped && ctx.Err() == nil {
			select {
			case <-process.done:
			case <-ctx.Done():
				process.stopErr = errors.Join(
					process.stopErr,
					fmt.Errorf("timed out waiting for %s process owner to stop: %w", process.name, ctx.Err()),
				)
			}
		}
		if process.watchdogCancel != nil {
			process.watchdogCancel()
		}
		if process.watchdogDone != nil {
			select {
			case <-process.watchdogDone:
			case <-ctx.Done():
				process.stopErr = errors.Join(
					process.stopErr,
					fmt.Errorf("timed out waiting for %s watchdog to stop: %w", process.name, ctx.Err()),
				)
			}
		}
	})
	return process.stopErr
}

func signalRuntimeProcessGroup(pid int, signal syscall.Signal) error {
	err := syscall.Kill(-pid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func runtimeProcessGroupAlive(pid int) (bool, error) {
	if pid <= 0 {
		return false, fmt.Errorf("invalid process group ID %d", pid)
	}
	err := syscall.Kill(-pid, 0)
	switch {
	case err == nil, errors.Is(err, syscall.EPERM):
		return true, nil
	case errors.Is(err, syscall.ESRCH):
		return false, nil
	default:
		return false, err
	}
}

func (process *runtimeProcess) setWaitErr(err error) {
	process.waitMu.Lock()
	defer process.waitMu.Unlock()
	process.waitErr = err
}

func (process *runtimeProcess) getWaitErr() error {
	process.waitMu.Lock()
	defer process.waitMu.Unlock()
	return process.waitErr
}

const (
	runtimeFocusedLogLineLimit = 120
	runtimeTailLogLineLimit    = 120
)

var runtimeFocusedLogTerms = []string{
	"file ingest lifecycle",
	"File ingest lifecycle",
	"Published file ingest",
	"Broadcasted file ingest",
	"media processing lifecycle",
	"projection",
	"uploadAttempt",
	"pendingUploadFileId",
	"data-pending-upload-file-id",
	"Upload attempt was not cleared",
	`"level":"WARN"`,
	`"level":"ERROR"`,
	" WARN ",
	" ERROR ",
	"Failed",
	"failed",
}

var runtimeFocusedLogExcludeTerms = []string{
	"127.0.0.1:4318",
	"localhost:4318",
	"host.docker.internal:4318",
	testcontainers.HostInternal + ":4318",
	"opentelemetry",
	"failed to upload metrics",
	"failed to upload traces",
}

func runtimeFocusedLogLines(data string, terms []string, limit int) string {
	if len(terms) == 0 {
		return ""
	}
	lines := strings.Split(data, "\n")
	matches := make([]string, 0, limit)
	for _, line := range lines {
		if line == "" {
			continue
		}
		if runtimeLogLineExcluded(line) {
			continue
		}
		for _, term := range terms {
			if strings.Contains(line, term) {
				matches = append(matches, line)
				break
			}
		}
	}
	if len(matches) > limit {
		matches = matches[len(matches)-limit:]
	}
	return strings.Join(matches, "\n")
}

func runtimeLogLineExcluded(line string) bool {
	for _, term := range runtimeFocusedLogExcludeTerms {
		if strings.Contains(line, term) {
			return true
		}
	}
	return false
}

func runtimeTailLogLines(data string, limit int) string {
	lines := strings.Split(strings.TrimRight(data, "\n"), "\n")
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return strings.Join(lines, "\n")
}

func (s *RuntimeStack) DumpProcessLogs(t *testing.T) {
	t.Helper()

	type namedProcess struct {
		name string
		proc *runtimeProcess
	}

	processes := []namedProcess{
		{name: "collab", proc: s.collabProc},
		{name: "backend", proc: s.backendProc},
		{name: "transcoder", proc: s.transcoderProc},
		{name: "waveform", proc: s.waveformProc},
	}
	for _, entry := range processes {
		if entry.proc == nil || entry.proc.logPath == "" {
			continue
		}
		data, err := os.ReadFile(entry.proc.logPath)
		if err != nil {
			t.Logf("[%s log unavailable] %v", entry.name, err)
			continue
		}
		logText := string(data)
		if focused := runtimeFocusedLogLines(logText, runtimeFocusedLogTerms, runtimeFocusedLogLineLimit); focused != "" {
			t.Logf("[%s focused log]\n%s", entry.name, focused)
		}
		if tail := runtimeTailLogLines(logText, runtimeTailLogLineLimit); tail != "" {
			t.Logf("[%s log tail: last %d lines]\n%s", entry.name, runtimeTailLogLineLimit, tail)
		}
	}

	if s.imgproxyContainer != nil {
		logText := strings.TrimSpace(strings.TrimPrefix(backendIntegrationContainerLogs(context.Background(), s.imgproxyContainer), ": "))
		if logText != "" {
			if focused := runtimeFocusedLogLines(logText, runtimeFocusedLogTerms, runtimeFocusedLogLineLimit); focused != "" {
				t.Logf("[imgproxy focused log]\n%s", focused)
			}
			if tail := runtimeTailLogLines(logText, runtimeTailLogLineLimit); tail != "" {
				t.Logf("[imgproxy log tail: last %d lines]\n%s", runtimeTailLogLineLimit, tail)
			}
		}
	}
}

func waitForRuntimeHealth(t *testing.T, process *runtimeProcess) {
	t.Helper()

	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-process.done:
			t.Fatalf("process exited before becoming healthy: %v\nlog:\n%s", process.getWaitErr(), readRuntimeLog(process.logPath))
		default:
		}

		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, process.healthURL, nil)
		require.NoError(t, err)
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}

	select {
	case <-process.done:
		t.Fatalf("process exited before health deadline: %v\nlog:\n%s", process.getWaitErr(), readRuntimeLog(process.logPath))
	default:
	}
	t.Fatalf("process did not become healthy: %s\nlog:\n%s", process.healthURL, readRuntimeLog(process.logPath))
}

func readRuntimeLog(path string) string {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("failed to read log %s: %v", path, err)
	}
	if len(bytes) == 0 {
		return "(empty log)"
	}
	return string(bytes)
}

func formatRuntimeEnv(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for key, value := range env {
		out = append(out, key+"="+value)
	}
	return out
}

func reserveLocalPort(t *testing.T) int {
	t.Helper()

	for attempt := 0; attempt < 64; attempt++ {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		addr, ok := listener.Addr().(*net.TCPAddr)
		require.True(t, ok)
		port := addr.Port
		require.NoError(t, listener.Close())

		runtimeReservedPortMu.Lock()
		_, alreadyReserved := runtimeReservedPorts[port]
		if !alreadyReserved {
			runtimeReservedPorts[port] = struct{}{}
		}
		runtimeReservedPortMu.Unlock()
		if !alreadyReserved {
			return port
		}
	}

	t.Fatal("failed to reserve a unique local runtime port")
	return 0
}

func WaitForFileProcessingComplete(
	t *testing.T,
	timeout time.Duration,
	lookup func() (bool, string, error),
) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	var lastMessage string
	for time.Now().Before(deadline) {
		done, message, err := lookup()
		require.NoError(t, err)
		lastMessage = message
		if done {
			return
		}
		time.Sleep(750 * time.Millisecond)
	}

	if strings.TrimSpace(lastMessage) == "" {
		lastMessage = "processing did not complete before timeout"
	}
	t.Fatal(lastMessage)
}

func WaitForProcessHealthy(t *testing.T, process *runtimeProcess) {
	t.Helper()
	require.NotNil(t, process)
	waitForRuntimeHealth(t, process)
}

func WaitForProcessExit(process *runtimeProcess, timeout time.Duration) error {
	if process == nil {
		return nil
	}
	select {
	case <-process.done:
		return process.getWaitErr()
	case <-time.After(timeout):
		return errors.New("timed out waiting for process exit")
	}
}
