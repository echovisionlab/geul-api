package authzmutation

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
	authorizationBoundaryFailureCommitUncertain            = "commit_uncertain"
	authorizationBoundaryFailureRollbackCompensationFailed = "rollback_compensation_failed"
	authorizationCommitOutcomeSucceeded                    = "succeeded"
	authorizationCommitOutcomeUncertain                    = "commit_uncertain"
)

type synchronousAuthorizationMetrics struct {
	boundaryFailureCounter otelmetric.Int64Counter
	writeToCommitDuration  otelmetric.Float64Histogram
}

var defaultSynchronousAuthorizationMetrics = newSynchronousAuthorizationMetrics()

func newSynchronousAuthorizationMetrics() synchronousAuthorizationMetrics {
	meter := otel.Meter(sharedtelemetry.ServiceBackend.Instrumentation("authorization"))
	metrics := synchronousAuthorizationMetrics{}

	counter, err := meter.Int64Counter(
		"authorization_boundary_failure_total",
		otelmetric.WithDescription("Counts exceptional synchronous authorization transaction boundaries by bounded failure."),
	)
	if err != nil {
		slog.Warn("failed to create synchronous authorization boundary failure counter", "error", err)
	} else {
		metrics.boundaryFailureCounter = counter
	}

	writeToCommitDuration, err := meter.Float64Histogram(
		"authorization_spicedb_to_database_commit_duration_seconds",
		otelmetric.WithDescription("Records elapsed time from a confirmed SpiceDB relationship write to the database commit result."),
		otelmetric.WithExplicitBucketBoundaries(
			0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30,
		),
	)
	if err != nil {
		slog.Warn("failed to create SpiceDB-to-database commit duration histogram", "error", err)
	} else {
		metrics.writeToCommitDuration = writeToCommitDuration
	}

	return metrics
}

func (m synchronousAuthorizationMetrics) recordBoundaryFailure(ctx context.Context, failure string) {
	if m.boundaryFailureCounter == nil {
		return
	}
	m.boundaryFailureCounter.Add(ctx, 1, otelmetric.WithAttributes(attribute.String("failure", failure)))
}

func (m synchronousAuthorizationMetrics) recordWriteToCommitDuration(
	ctx context.Context,
	spiceDBConfirmedAt time.Time,
	outcome string,
) {
	if m.writeToCommitDuration == nil || spiceDBConfirmedAt.IsZero() {
		return
	}
	duration := time.Since(spiceDBConfirmedAt)
	if duration < 0 {
		duration = 0
	}
	m.writeToCommitDuration.Record(ctx, duration.Seconds(), otelmetric.WithAttributes(
		attribute.String("outcome", outcome),
	))
}

// RecordAuthorizationCommitSucceeded records the completed database side of a
// transaction whose SpiceDB relationship write was already confirmed.
func RecordAuthorizationCommitSucceeded(ctx context.Context, spiceDBConfirmedAt time.Time) {
	defaultSynchronousAuthorizationMetrics.recordWriteToCommitDuration(
		ctx,
		spiceDBConfirmedAt,
		authorizationCommitOutcomeSucceeded,
	)
}

// RecordAuthorizationCommitUncertain records a database commit whose result is
// unknown after an authorization mutation was confirmed.
func RecordAuthorizationCommitUncertain(ctx context.Context, spiceDBConfirmedAt time.Time) {
	if spiceDBConfirmedAt.IsZero() {
		return
	}
	defaultSynchronousAuthorizationMetrics.recordBoundaryFailure(
		ctx,
		authorizationBoundaryFailureCommitUncertain,
	)
	defaultSynchronousAuthorizationMetrics.recordWriteToCommitDuration(
		ctx,
		spiceDBConfirmedAt,
		authorizationCommitOutcomeUncertain,
	)
}

// RecordAuthorizationRollbackCompensationFailed records a failed attempt to
// restore authorization relationships before a known database rollback.
func RecordAuthorizationRollbackCompensationFailed(ctx context.Context) {
	defaultSynchronousAuthorizationMetrics.recordBoundaryFailure(
		ctx,
		authorizationBoundaryFailureRollbackCompensationFailed,
	)
}
