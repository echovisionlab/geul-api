package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/config"
	"github.com/echovisionlab/geul-api/internal/telemetry"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

const healthReadinessTimeout = 2 * time.Second

type healthReadinessCheck func(context.Context) error

func run() int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtimeFailures := make(chan error, 1)

	stdoutHandler := configureBootstrapLogging()
	cfg, err := config.Load()
	if err != nil {
		return logStartupFailure("load config", err)
	}
	otelResult, err := telemetry.Init(ctx, sharedtelemetry.ServiceBackend)
	if err != nil {
		return logStartupFailure("initialize telemetry", err)
	}
	defer otelResult.Shutdown(ctx)
	slog.SetDefault(slog.New(telemetry.NewNormalizingHandler(
		telemetry.NewFanoutHandler(stdoutHandler, otelResult.LogHandler),
	)))

	deps, err := initializeApplicationDependencies(ctx, cfg)
	if err != nil {
		return logStartupFailure("initialize application dependencies", err)
	}
	defer deps.Close()

	mux := http.NewServeMux()
	registerHealthRoutes(mux, newPostgresPGMQReadinessCheck(deps.sqlDB))
	if err := registerAuthenticationRoutes(mux, cfg, deps); err != nil {
		return logStartupFailure("register authentication routes", err)
	}
	registered, err := registerServices(serviceRegistrationDependencies{
		mux:                     mux,
		cfg:                     cfg,
		db:                      deps.db,
		s3Client:                deps.s3Client,
		s3PresignClient:         deps.s3PresignClient,
		servicePublisher:        deps.servicePublisher,
		workerPublisher:         deps.workerPublisher,
		hooksPublisher:          deps.hooksPublisher,
		ogPublisher:             deps.ogPublisher,
		transcodeTracker:        deps.transcodeTracker,
		spicedbClient:           deps.spicedbClient,
		kratosClient:            deps.kratosClient,
		passwordHasher:          deps.passwordHasher,
		metadataAIJobs:          deps.metadataAIJobs,
		contentBlockStore:       deps.contentBlockStore,
		authCodeIssuanceLimiter: deps.authCodeIssuanceLimiter,
		adapterLoader:           deps.adapterLoader,
		telemetryWriter:         deps.telemetryWriter,
		og:                      deps.og,
	})
	if err != nil {
		return logStartupFailure("register services", err)
	}

	runtime, err := initializeApplicationRuntime(ctx, cfg, deps, runtimeFailures)
	if err != nil {
		return logStartupFailure("initialize application runtime", err)
	}
	shutdownComplete := false
	defer func() {
		if !shutdownComplete {
			runtime.Close()
		}
	}()

	server := newApplicationHTTPServer(mux, cfg)
	mcpPrivateServer, err := newMCPPrivateHTTPServer(mcpPrivateHandlers{
		authorAdmission: registered.mcpAuthorAdmissionHandler,
	}, cfg)
	if err != nil {
		return logStartupFailure("initialize MCP private HTTP server", err)
	}
	runtime.Start(ctx, server, mcpPrivateServer, registered.authInterceptor, deps, registered, runtimeFailures)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	sig, runtimeErr := awaitShutdown(ctx, cancel, signals, runtimeFailures)
	logShutdownReason(sig, runtimeErr)

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	shutdownComplete = true
	if !runtime.Shutdown(shutdownCtx, server, mcpPrivateServer) {
		slog.Error("Background worker shutdown timed out")
	}
	slog.Info("Server stopped")
	if runtimeErr != nil {
		return 1
	}
	return 0
}

func logStartupFailure(component string, err error) int {
	slog.Error("Application startup failed", "component", component, "error", err)
	return 1
}

func registerHealthRoutes(mux *http.ServeMux, ready healthReadinessCheck) {
	writeLive := func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
		fmt.Fprintln(writer, "ok")
	}
	mux.HandleFunc("/health/live", writeLive)

	writeReady := func(writer http.ResponseWriter, request *http.Request) {
		ctx, cancel := context.WithTimeout(request.Context(), healthReadinessTimeout)
		defer cancel()
		if ready == nil || ready(ctx) != nil {
			http.Error(writer, "not ready", http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusOK)
		fmt.Fprintln(writer, "ok")
	}
	// /health remains the deployed readiness path. The explicit routes let a
	// future orchestrator distinguish process liveness from dependency readiness.
	mux.HandleFunc("/health", writeReady)
	mux.HandleFunc("/health/ready", writeReady)
}

func newPostgresPGMQReadinessCheck(db *sql.DB) healthReadinessCheck {
	return func(ctx context.Context) error {
		if db == nil {
			return fmt.Errorf("PostgreSQL connection is required")
		}
		if err := db.PingContext(ctx); err != nil {
			return fmt.Errorf("ping PostgreSQL: %w", err)
		}

		var queueName string
		if err := db.QueryRowContext(
			ctx,
			"SELECT queue_name FROM pgmq.metrics($1)",
			eventpkg.QueueEmailSend,
		).Scan(&queueName); err != nil {
			return fmt.Errorf("verify PGMQ queue %s: %w", eventpkg.QueueEmailSend, err)
		}
		if queueName != eventpkg.QueueEmailSend {
			return fmt.Errorf("PGMQ queue mismatch: got %q", queueName)
		}
		return nil
	}
}

func logShutdownReason(signal os.Signal, runtimeErr error) {
	switch {
	case signal != nil:
		slog.Info("Received signal, shutting down", "signal", signal)
	case runtimeErr != nil:
		slog.Error("Required runtime component failed; shutting down", "error", runtimeErr)
	default:
		slog.Info("Context cancelled, shutting down")
	}
}
func main() {
	os.Exit(run())
}

type internalRPCTrustBoundary struct {
	secret                    string
	internalServiceHeaderName string
}

func (b internalRPCTrustBoundary) collab(next http.Handler) http.Handler {
	return auth.RequireInternalServiceSecret(b.secret, b.internalServiceHeaderName, withSystemActor(sharedtelemetry.ServiceEditorCollab, next))
}

func (b internalRPCTrustBoundary) identity(next http.Handler) http.Handler {
	return auth.RequireInternalServiceSecret(b.secret, b.internalServiceHeaderName, withSystemActor(sharedtelemetry.ServiceIdentity, next))
}

func (b internalRPCTrustBoundary) og(next http.Handler) http.Handler {
	return auth.RequireInternalServiceSecret(b.secret, b.internalServiceHeaderName, withSystemActor(sharedtelemetry.ServiceOG, next))
}

func withSystemActor(serviceName sharedtelemetry.ServiceName, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := telemetry.WithActor(r.Context(), telemetry.SystemActor{ServiceName: serviceName})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func reportRuntimeFailure(failures chan<- error, err error) {
	if err == nil {
		return
	}
	select {
	case failures <- err:
	default:
	}
}

func waitForShutdownWorkers(ctx context.Context, wg *sync.WaitGroup) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

func awaitShutdown(
	ctx context.Context,
	cancel context.CancelFunc,
	signals <-chan os.Signal,
	runtimeFailures <-chan error,
) (os.Signal, error) {
	defer cancel()
	select {
	case signal := <-signals:
		return signal, nil
	case runtimeErr := <-runtimeFailures:
		return nil, runtimeErr
	case <-ctx.Done():
		return nil, nil
	}
}
