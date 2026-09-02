package application

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

type translationMetrics struct {
	queuedJobCounter     otelmetric.Int64Counter
	jobStatusCounter     otelmetric.Int64Counter
	jobDurationHistogram otelmetric.Int64Histogram
	adminActionCounter   otelmetric.Int64Counter
	ogHandoffCounter     otelmetric.Int64Counter
}

func newTranslationMetrics() translationMetrics {
	meter := otel.Meter(sharedtelemetry.ServiceBackend.Instrumentation("translation"))
	metrics := translationMetrics{}

	queuedJobCounter, err := meter.Int64Counter(
		"translation_jobs_queued_total",
		otelmetric.WithDescription("Counts explicit translation commands queued by entity type and locale."),
	)
	if err != nil {
		slog.Warn("failed to create translation queued job counter", "error", err)
	} else {
		metrics.queuedJobCounter = queuedJobCounter
	}

	jobStatusCounter, err := meter.Int64Counter(
		"translation_job_status_total",
		otelmetric.WithDescription("Counts translation job status transitions by entity type, locale, and status."),
	)
	if err != nil {
		slog.Warn("failed to create translation job status counter", "error", err)
	} else {
		metrics.jobStatusCounter = jobStatusCounter
	}

	jobDurationHistogram, err := meter.Int64Histogram(
		"translation_job_duration_seconds",
		otelmetric.WithDescription("Records end-to-end translation job duration in seconds for final states."),
	)
	if err != nil {
		slog.Warn("failed to create translation job duration histogram", "error", err)
	} else {
		metrics.jobDurationHistogram = jobDurationHistogram
	}

	adminActionCounter, err := meter.Int64Counter(
		"translation_admin_action_total",
		otelmetric.WithDescription("Counts explicit Translation administrator actions by action and outcome."),
	)
	if err != nil {
		slog.Warn("failed to create translation administrator action counter", "error", err)
	} else {
		metrics.adminActionCounter = adminActionCounter
	}

	ogHandoffCounter, err := meter.Int64Counter(
		"translation_og_handoff_total",
		otelmetric.WithDescription("Counts authoritative translation-to-OG handoff transaction outcomes."),
	)
	if err != nil {
		slog.Warn("failed to create translation OG handoff counter", "error", err)
	} else {
		metrics.ogHandoffCounter = ogHandoffCounter
	}

	return metrics
}

func (m translationMetrics) recordQueuedJob(ctx context.Context, job *model.TranslationJob) {
	if m.queuedJobCounter == nil || job == nil {
		return
	}
	m.queuedJobCounter.Add(ctx, 1, otelmetric.WithAttributes(
		attribute.String("entity_type", job.EntityType),
		attribute.String("target_locale", job.TargetLocale),
	))
}

func (m translationMetrics) recordAdminAction(ctx context.Context, action string, outcome string) {
	if m.adminActionCounter == nil {
		return
	}
	m.adminActionCounter.Add(ctx, 1, otelmetric.WithAttributes(
		attribute.String("action", action),
		attribute.String("outcome", outcome),
	))
}

func (m translationMetrics) recordOgHandoff(ctx context.Context, job *model.TranslationJob, outcome string) {
	if m.ogHandoffCounter == nil || job == nil {
		return
	}
	m.ogHandoffCounter.Add(ctx, 1, otelmetric.WithAttributes(
		attribute.String("entity_type", job.EntityType),
		attribute.String("target_locale", job.TargetLocale),
		attribute.String("outcome", outcome),
	))
}

func (m translationMetrics) recordJobStatus(
	ctx context.Context,
	job *model.TranslationJob,
	status string,
) {
	if m.jobStatusCounter == nil || job == nil {
		return
	}

	attributes := []attribute.KeyValue{
		attribute.String("entity_type", job.EntityType),
		attribute.String("target_locale", job.TargetLocale),
		attribute.String("status", status),
	}
	if status == translationJobStatusFailed && job.FailureReason != nil {
		attributes = append(attributes, attribute.String(
			"failure_reason", boundedTranslationFailureReasonLabel(job.FailureReason),
		))
	}
	m.jobStatusCounter.Add(ctx, 1, otelmetric.WithAttributes(attributes...))
}

func boundedTranslationFailureReasonLabel(reason *string) string {
	if reason == nil {
		return translationFailureInternal
	}
	switch normalized := strings.TrimSpace(*reason); normalized {
	case translationFailureProviderConfiguration,
		translationFailureProviderAuthentication,
		translationFailureProviderRateLimited,
		translationFailureProviderUnavailable,
		translationFailureProviderRejected,
		translationFailureProviderResponseInvalid,
		translationFailureTargetApplyFailed,
		translationFailureOgHandoffFailed,
		translationFailureInternal:
		return normalized
	default:
		return translationFailureInternal
	}
}

func (m translationMetrics) recordJobDuration(
	ctx context.Context,
	job *model.TranslationJob,
	status string,
	startedAt time.Time,
	finishedAt time.Time,
) {
	if m.jobDurationHistogram == nil || job == nil || startedAt.IsZero() || finishedAt.Before(startedAt) {
		return
	}

	m.jobDurationHistogram.Record(
		ctx,
		int64(finishedAt.Sub(startedAt).Seconds()),
		otelmetric.WithAttributes(
			attribute.String("entity_type", job.EntityType),
			attribute.String("target_locale", job.TargetLocale),
			attribute.String("status", status),
		),
	)
}
