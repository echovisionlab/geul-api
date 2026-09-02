package auth

import (
	"context"
	"log/slog"
	"time"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

const (
	spiceDBOutcomeAllowed   = "allowed"
	spiceDBOutcomeDenied    = "denied"
	spiceDBOutcomeFailed    = "failed"
	spiceDBOutcomeSucceeded = "succeeded"
	spiceDBOutcomeUncertain = "uncertain"

	spiceDBWriteOperationBatch    = "batch_write"
	spiceDBWriteOperationResource = "resource_delete"
	spiceDBWriteOperationSubject  = "subject_delete"
)

type spiceDBMetrics struct {
	checkCounter  otelmetric.Int64Counter
	checkDuration otelmetric.Float64Histogram
	writeCounter  otelmetric.Int64Counter
	writeDuration otelmetric.Float64Histogram
}

var defaultSpiceDBMetrics = newSpiceDBMetrics()

func newSpiceDBMetrics() spiceDBMetrics {
	meter := otel.Meter(sharedtelemetry.ServiceBackend.Instrumentation("authorization"))
	metrics := spiceDBMetrics{}

	checkCounter, err := meter.Int64Counter(
		"authorization_spicedb_check_total",
		otelmetric.WithDescription("Counts final SpiceDB permission checks by bounded outcome."),
	)
	if err != nil {
		slog.Warn("failed to create SpiceDB authorization check counter", "error", err)
	} else {
		metrics.checkCounter = checkCounter
	}

	checkDuration, err := meter.Float64Histogram(
		"authorization_spicedb_check_duration_seconds",
		otelmetric.WithDescription("Records final SpiceDB permission-check latency by bounded outcome."),
	)
	if err != nil {
		slog.Warn("failed to create SpiceDB authorization check duration histogram", "error", err)
	} else {
		metrics.checkDuration = checkDuration
	}

	writeCounter, err := meter.Int64Counter(
		"authorization_spicedb_write_total",
		otelmetric.WithDescription("Counts SpiceDB relationship mutations by bounded operation and outcome."),
	)
	if err != nil {
		slog.Warn("failed to create SpiceDB authorization write counter", "error", err)
	} else {
		metrics.writeCounter = writeCounter
	}

	writeDuration, err := meter.Float64Histogram(
		"authorization_spicedb_write_duration_seconds",
		otelmetric.WithDescription("Records SpiceDB relationship-mutation latency by bounded operation and outcome."),
	)
	if err != nil {
		slog.Warn("failed to create SpiceDB authorization write duration histogram", "error", err)
	} else {
		metrics.writeDuration = writeDuration
	}

	return metrics
}

func (m spiceDBMetrics) recordCheck(ctx context.Context, startedAt time.Time, outcome string) {
	attributes := otelmetric.WithAttributes(attribute.String("outcome", outcome))
	if m.checkCounter != nil {
		m.checkCounter.Add(ctx, 1, attributes)
	}
	if m.checkDuration != nil {
		m.checkDuration.Record(ctx, time.Since(startedAt).Seconds(), attributes)
	}
}

func (m spiceDBMetrics) recordWrite(ctx context.Context, startedAt time.Time, operation string, outcome string) {
	attributes := otelmetric.WithAttributes(
		attribute.String("operation", operation),
		attribute.String("outcome", outcome),
	)
	if m.writeCounter != nil {
		m.writeCounter.Add(ctx, 1, attributes)
	}
	if m.writeDuration != nil {
		m.writeDuration.Record(ctx, time.Since(startedAt).Seconds(), attributes)
	}
}

func spiceDBWriteOutcome(err error) string {
	if err == nil {
		return spiceDBOutcomeSucceeded
	}
	if IsRelationshipWriteOutcomeUncertain(err) {
		return spiceDBOutcomeUncertain
	}
	return spiceDBOutcomeFailed
}
