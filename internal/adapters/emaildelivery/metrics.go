package emaildeliveryadapter

import (
	"context"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/model"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

type Metrics struct {
	sendAttemptCounter     otelmetric.Int64Counter
	sendResultCounter      otelmetric.Int64Counter
	recipientStatusCounter otelmetric.Int64Counter
	runDurationHistogram   otelmetric.Float64Histogram
}

func NewMetrics() Metrics {
	meter := otel.Meter(sharedtelemetry.ServiceBackend.Instrumentation("mail"))
	metrics := Metrics{}
	var err error
	metrics.sendAttemptCounter, err = meter.Int64Counter(
		"mail_send_attempt_total",
		otelmetric.WithDescription("Counts mail send attempts by template class."),
	)
	if err != nil {
		slog.Warn("failed to create mail send attempt counter", "error", err)
	}
	metrics.sendResultCounter, err = meter.Int64Counter(
		"mail_send_result_total",
		otelmetric.WithDescription("Counts mail send results by template class and result."),
	)
	if err != nil {
		slog.Warn("failed to create mail send result counter", "error", err)
	}
	metrics.recipientStatusCounter, err = meter.Int64Counter(
		"mail_delivery_recipient_status_total",
		otelmetric.WithDescription("Counts bulk mail recipient status transitions by template class and status."),
	)
	if err != nil {
		slog.Warn("failed to create mail recipient status counter", "error", err)
	}
	metrics.runDurationHistogram, err = meter.Float64Histogram(
		"mail_delivery_run_duration_seconds",
		otelmetric.WithDescription("Records completed bulk mail delivery run duration by run kind and status."),
	)
	if err != nil {
		slog.Warn("failed to create mail delivery run duration histogram", "error", err)
	}
	return metrics
}

func (m Metrics) RecordSendAttempt(ctx context.Context, templateType string) {
	if m.sendAttemptCounter != nil {
		m.sendAttemptCounter.Add(ctx, 1, otelmetric.WithAttributes(
			attribute.String("template_class", TemplateClass(templateType)),
		))
	}
}

func (m Metrics) RecordSendResult(ctx context.Context, templateType string, result string) {
	if m.sendResultCounter != nil {
		m.sendResultCounter.Add(ctx, 1, otelmetric.WithAttributes(
			attribute.String("template_class", TemplateClass(templateType)),
			attribute.String("result", result),
		))
	}
}

func (m Metrics) RecordRecipientStatus(ctx context.Context, templateType string, status string) {
	if m.recipientStatusCounter != nil {
		m.recipientStatusCounter.Add(ctx, 1, otelmetric.WithAttributes(
			attribute.String("template_class", TemplateClass(templateType)),
			attribute.String("status", status),
		))
	}
}

func (m Metrics) RecordRunDuration(ctx context.Context, run model.CampaignDeliveryRun, completedAt time.Time) {
	if m.runDurationHistogram == nil {
		return
	}
	startedAt := run.CreatedAt
	if run.StartedAt != nil && !run.StartedAt.IsZero() {
		startedAt = *run.StartedAt
	}
	if completedAt.Before(startedAt) {
		return
	}
	m.runDurationHistogram.Record(ctx, completedAt.Sub(startedAt).Seconds(), otelmetric.WithAttributes(
		attribute.String("run_kind", run.RunKind),
		attribute.String("status", run.Status),
	))
}

func TemplateClass(templateType string) string {
	return string(emailauthoring.ClassifyEmailTemplateType(templateType))
}
