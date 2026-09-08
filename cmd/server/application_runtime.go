package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/rs/cors"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/config"
	"github.com/echovisionlab/geul-api/internal/mediaruntime"
	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/scheduler"
	"github.com/echovisionlab/geul-api/internal/telemetry"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

type applicationRuntime struct {
	ogProcess                          *mediaruntime.OGProcess
	mediaWorkers                       *mediaruntime.Workers
	mediaServer                        *http.Server
	mediaListener                      net.Listener
	consumerManager                    *mq.ConsumerManager
	transcodeProgressSubscriber        *mq.BroadcastSubscriber
	waveformProgressSubscriber         *mq.BroadcastSubscriber
	meshOptimizationProgressSubscriber *mq.BroadcastSubscriber
	mailAdapterSubscriber              *mq.BroadcastSubscriber
	scheduler                          *scheduler.Scheduler
	wg                                 sync.WaitGroup
}

func initializeApplicationRuntime(
	ctx context.Context,
	cfg *config.Config,
	deps *applicationDependencies,
	runtimeFailures chan<- error,
) (result *applicationRuntime, resultErr error) {
	runtime := &applicationRuntime{}
	defer func() {
		if resultErr != nil {
			runtime.Close()
		}
	}()

	runtime.consumerManager = mq.NewConsumerManager(deps.sqlDB)
	if err := deps.workerHandlers.RegisterQueues(runtime.consumerManager); err != nil {
		return nil, fmt.Errorf("register queue handlers: %w", err)
	}
	if err := runtime.startRequiredConsumers(ctx, runtimeFailures); err != nil {
		return nil, err
	}
	if err := runtime.initializeSubscribers(deps); err != nil {
		return nil, err
	}

	runtime.mediaServer, resultErr = newMediaHTTPServer(cfg)
	if resultErr != nil {
		return nil, resultErr
	}
	runtime.mediaListener, resultErr = net.Listen("tcp", runtime.mediaServer.Addr)
	if resultErr != nil {
		return nil, resultErr
	}
	runtime.wg.Go(func() {
		if err := runtime.mediaServer.Serve(runtime.mediaListener); err != nil && err != http.ErrServerClosed {
			reportRuntimeFailure(runtimeFailures, fmt.Errorf("media delivery: %w", err))
		}
	})
	runtime.mediaWorkers, resultErr = mediaruntime.StartWorkers(ctx, cfg)
	if resultErr != nil {
		return nil, fmt.Errorf("initialize media runtime: %w", resultErr)
	}
	runtime.ogProcess, resultErr = mediaruntime.NewOGProcess(cfg)
	if resultErr != nil {
		return nil, resultErr
	}
	runtime.initializeScheduler(cfg, deps)
	return runtime, nil
}

func (r *applicationRuntime) startRequiredConsumers(
	ctx context.Context,
	runtimeFailures chan<- error,
) error {
	ready := make(chan error, 1)
	r.wg.Go(func() {
		slog.Info("Consumer manager starting")
		if err := r.consumerManager.StartWithReady(ctx, ready); err != nil {
			slog.Error("Consumer manager error", "error", err)
			reportRuntimeFailure(runtimeFailures, fmt.Errorf("consumer manager: %w", err))
		}
	})
	if err := <-ready; err != nil {
		return fmt.Errorf("PGMQ consumer readiness: %w", err)
	}
	slog.Info("Required PGMQ queues ready", "queues", r.consumerManager.QueueNames())
	return nil
}

func (r *applicationRuntime) initializeSubscribers(deps *applicationDependencies) error {
	var err error
	r.transcodeProgressSubscriber, err = mq.NewBroadcastSubscriber(
		deps.databaseDSN,
		eventpkg.SignalTranscodeProgress,
		deps.workerHandlers.HandleTranscodeProgress,
	)
	if err != nil {
		return fmt.Errorf("initialize transcode progress subscriber: %w", err)
	}
	r.waveformProgressSubscriber, err = mq.NewBroadcastSubscriber(
		deps.databaseDSN,
		eventpkg.SignalWaveformProgress,
		deps.workerHandlers.HandleWaveformProgress,
	)
	if err != nil {
		return fmt.Errorf("initialize waveform progress subscriber: %w", err)
	}
	r.meshOptimizationProgressSubscriber, err = mq.NewBroadcastSubscriber(
		deps.databaseDSN,
		eventpkg.SignalMeshOptimizationProgress,
		deps.workerHandlers.HandleMeshOptimizationProgress,
	)
	if err != nil {
		return fmt.Errorf("initialize mesh optimization progress subscriber: %w", err)
	}
	r.mailAdapterSubscriber, err = mq.NewBroadcastSubscriber(
		deps.databaseDSN,
		eventpkg.SignalMailAdapterChanged,
		deps.adapterLoader.HandleMailAdapterChanged,
	)
	if err != nil {
		return fmt.Errorf("initialize mail adapter subscriber: %w", err)
	}
	return nil
}

func (r *applicationRuntime) initializeScheduler(cfg *config.Config, deps *applicationDependencies) {
	leader := scheduler.NewPostgresLeader(deps.sqlDB, cfg.InstanceID)
	r.scheduler = scheduler.NewScheduler(
		scheduler.JobPusherFunc(deps.workerHandlers.HandleScheduled),
		leader,
		cfg.InstanceID,
	)
	slog.Info("Scheduler initialized", "instanceId", cfg.InstanceID)
}

func (r *applicationRuntime) Start(
	ctx context.Context,
	server *http.Server,
	mcpPrivateServer *http.Server,
	authInterceptor interface{ Start(context.Context) },
	deps *applicationDependencies,
	registered registeredServices,
	runtimeFailures chan<- error,
) {
	r.wg.Go(func() { authInterceptor.Start(ctx) })
	if !r.startHTTPServer("HTTP server", server, runtimeFailures) {
		return
	}
	if !r.startHTTPServer("MCP private HTTP server", mcpPrivateServer, runtimeFailures) {
		return
	}
	if err := r.ogProcess.Start(func(err error) { reportRuntimeFailure(runtimeFailures, err) }); err != nil {
		reportRuntimeFailure(runtimeFailures, err)
		return
	}
	r.startSubscriber(ctx, "Transcode progress", r.transcodeProgressSubscriber)
	r.startSubscriber(ctx, "Waveform progress", r.waveformProgressSubscriber)
	r.startSubscriber(ctx, "Mesh optimization progress", r.meshOptimizationProgressSubscriber)
	r.startSubscriber(ctx, "Mail adapter", r.mailAdapterSubscriber)
	r.wg.Go(func() { registered.accountEmailChangeLifecycle.Start(ctx) })
	r.wg.Go(func() { registered.accountLifecycle.Start(ctx) })
	if r.scheduler != nil {
		r.wg.Go(func() { r.scheduler.Start(ctx) })
	}
}

func (r *applicationRuntime) startHTTPServer(name string, server *http.Server, runtimeFailures chan<- error) bool {
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		reportRuntimeFailure(runtimeFailures, fmt.Errorf("%s: %w", name, err))
		return false
	}
	r.wg.Go(func() {
		slog.Info(name+" starting", "address", server.Addr)
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			reportRuntimeFailure(runtimeFailures, fmt.Errorf("%s: %w", name, err))
		}
	})
	return true
}

func newApplicationHTTPServer(mux *http.ServeMux, cfg *config.Config) *http.Server {
	corsHandler := cors.New(cors.Options{
		AllowedOrigins:   cfg.CORSOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "Connect-Protocol-Version", "MCP-Protocol-Version"},
		ExposedHeaders:   []string{"Grpc-Status", "Grpc-Message", "Retry-After", telemetry.RequestIDHeader},
		AllowCredentials: true,
		MaxAge:           300,
	})
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	return &http.Server{
		Addr: fmt.Sprintf(":%d", cfg.Port),
		Handler: corsHandler.Handler(telemetry.NewHTTPHandler(mux, func(request *http.Request) bool {
			return auth.IsInternalServiceRequest(cfg.TokenSigningSecret, cfg.InternalServiceHeaderName, request)
		})),
		Protocols:    protocols,
		ReadTimeout:  time.Duration(cfg.HTTPReadTimeoutSec) * time.Second,
		WriteTimeout: time.Duration(cfg.HTTPWriteTimeoutSec) * time.Second,
		IdleTimeout:  time.Duration(cfg.HTTPIdleTimeoutSec) * time.Second,
	}
}

func (r *applicationRuntime) startSubscriber(
	ctx context.Context,
	name string,
	subscriber *mq.BroadcastSubscriber,
) {
	r.wg.Go(func() {
		slog.Info(name + " subscriber starting")
		if err := subscriber.Start(ctx); err != nil {
			slog.Error(name+" subscriber error", "error", err)
		}
	})
}

func (r *applicationRuntime) Shutdown(
	ctx context.Context,
	server *http.Server,
	mcpPrivateServer *http.Server,
) bool {
	if r.ogProcess != nil {
		if err := r.ogProcess.Shutdown(ctx); err != nil {
			slog.Error("OG shutdown error", "error", err)
		}
	}
	if r.mediaWorkers != nil {
		_ = r.mediaWorkers.Close()
	}
	if r.mediaServer != nil {
		shutdownHTTPServer(ctx, "Media delivery", r.mediaServer)
	}
	shutdownHTTPServer(ctx, "HTTP server", server)
	shutdownHTTPServer(ctx, "MCP private HTTP server", mcpPrivateServer)
	if r.scheduler != nil {
		r.scheduler.Stop()
	}
	if r.consumerManager != nil {
		r.consumerManager.Close()
	}
	closeSubscriber(r.transcodeProgressSubscriber)
	closeSubscriber(r.waveformProgressSubscriber)
	closeSubscriber(r.meshOptimizationProgressSubscriber)
	closeSubscriber(r.mailAdapterSubscriber)
	return waitForShutdownWorkers(ctx, &r.wg)
}

func shutdownHTTPServer(ctx context.Context, name string, server *http.Server) {
	if err := server.Shutdown(ctx); err != nil {
		slog.Error(name+" shutdown error", "error", err)
	}
}

func (r *applicationRuntime) Close() {
	if r.ogProcess != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = r.ogProcess.Shutdown(ctx)
		cancel()
	}
	if r.mediaWorkers != nil {
		_ = r.mediaWorkers.Close()
	}
	if r.mediaServer != nil {
		_ = r.mediaServer.Close()
	}
	if r.mediaListener != nil {
		_ = r.mediaListener.Close()
	}
	if r.scheduler != nil {
		r.scheduler.Stop()
	}
	if r.consumerManager != nil {
		r.consumerManager.Close()
	}
	closeSubscriber(r.transcodeProgressSubscriber)
	closeSubscriber(r.waveformProgressSubscriber)
	closeSubscriber(r.meshOptimizationProgressSubscriber)
	closeSubscriber(r.mailAdapterSubscriber)
}

func closeSubscriber(subscriber *mq.BroadcastSubscriber) {
	if subscriber != nil {
		subscriber.Close()
	}
}
