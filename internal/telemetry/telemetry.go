package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// InitResult holds the telemetry initialization results.
type InitResult struct {
	// Shutdown flushes all pending telemetry data.
	Shutdown func(context.Context) error
	// LogHandler is an slog.Handler that bridges logs to OTel.
	LogHandler slog.Handler
}

// Init initializes OpenTelemetry with OTLP HTTP exporters.
// It reads OTEL_EXPORTER_OTLP_ENDPOINT and other standard OTel environment
// variables automatically via the SDK.
func Init(ctx context.Context, serviceName sharedtelemetry.ServiceName) (*InitResult, error) {
	canonicalServiceName, err := sharedtelemetry.ParseServiceName(serviceName.String())
	if err != nil {
		return nil, err
	}
	serviceNameValue := canonicalServiceName.String()
	res, err := newCanonicalResource(ctx, canonicalServiceName)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	// Trace provider
	traceExporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create trace exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	// Propagator (W3C Trace Context + Baggage)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// Metric provider
	metricExporter, err := otlpmetrichttp.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create metric exporter: %w", err)
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(mp)

	// Log provider
	logExporter, err := otlploghttp.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create log exporter: %w", err)
	}
	lp := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(logExporter)),
		sdklog.WithResource(res),
	)
	global.SetLoggerProvider(lp)

	// Create otelslog bridge handler
	logHandler := otelslog.NewHandler(serviceNameValue, otelslog.WithLoggerProvider(lp))

	slog.Info("OpenTelemetry initialized", "service", serviceNameValue)

	result := &InitResult{
		LogHandler: logHandler,
		Shutdown: func(ctx context.Context) error {
			var firstErr error
			if err := tp.Shutdown(ctx); err != nil {
				slog.Log(ctx, ShutdownLogLevel(err), "Telemetry provider shutdown failed", "provider", "trace", "failure_cause", failurecause.Message(err))
				if firstErr == nil {
					firstErr = fmt.Errorf("trace provider shutdown: %w", err)
				}
			}
			if err := mp.Shutdown(ctx); err != nil {
				slog.Log(ctx, ShutdownLogLevel(err), "Telemetry provider shutdown failed", "provider", "metric", "failure_cause", failurecause.Message(err))
				if firstErr == nil {
					firstErr = fmt.Errorf("metric provider shutdown: %w", err)
				}
			}
			if err := lp.Shutdown(ctx); err != nil {
				slog.Log(ctx, ShutdownLogLevel(err), "Telemetry provider shutdown failed", "provider", "log", "failure_cause", failurecause.Message(err))
				if firstErr == nil {
					firstErr = fmt.Errorf("log provider shutdown: %w", err)
				}
			}
			return firstErr
		},
	}
	return result, nil
}

// ShutdownLogLevel selects the severity for a failure while the process stops.
// Every rollout stops pods, so an exhausted or canceled shutdown context is an
// expected outcome of the stop itself. Other failures remain errors.
func ShutdownLogLevel(err error) slog.Level {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return slog.LevelInfo
	}
	return slog.LevelError
}

func newCanonicalResource(ctx context.Context, serviceName sharedtelemetry.ServiceName) (*resource.Resource, error) {
	return resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithHost(),
		resource.WithAttributes(semconv.ServiceName(serviceName.String())),
	)
}
