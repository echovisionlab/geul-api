package application

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/translation"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

func (m *TranslationJobManager) handleQueuedDeliveryFailure(
	ctx context.Context,
	job *model.TranslationJob,
	cause error,
) error {
	logCause := cause
	var pinned translationProviderDocumentPinnedError
	if errors.As(cause, &pinned) {
		logCause = pinned.Unwrap()
	}
	fields := translationJobFailedLogFields(ctx, job, cause, logCause)
	level := slog.LevelError
	if translationDeliveryTransportStopped(ctx, cause) {
		level = slog.LevelWarn
	}
	slog.Log(ctx, level, "Translation job failed", fields...)
	cause = boundTranslationFailure(cause)
	if translationDeliveryTransportStopped(ctx, cause) {
		return cause
	}
	persistCtx, cancel := translationTerminalCleanupContext(ctx)
	defer cancel()
	if err := m.failQueuedJob(persistCtx, job, cause); err != nil {
		return err
	}
	return mq.NewTerminalDeliveryError("translation_generation_failed", cause)
}

func (m *TranslationJobManager) handleRunningDeliveryFailure(
	ctx context.Context,
	job *model.TranslationJob,
	startedAt time.Time,
	cause error,
) error {
	logCause := cause
	var pinned translationProviderDocumentPinnedError
	if errors.As(cause, &pinned) {
		logCause = pinned.Unwrap()
	}
	fields := translationJobFailedLogFields(ctx, job, cause, logCause)
	level := slog.LevelError
	if translationDeliveryTransportStopped(ctx, cause) {
		level = slog.LevelWarn
	}
	slog.Log(ctx, level, "Translation job failed", fields...)
	cause = boundTranslationFailure(cause)
	if translationDeliveryTransportStopped(ctx, cause) {
		return cause
	}
	persistCtx, cancel := translationTerminalCleanupContext(ctx)
	defer cancel()
	if err := m.failJob(persistCtx, job, startedAt, cause); err != nil {
		if errors.Is(err, errTranslationJobNoLongerCurrent) {
			terminal, terminalErr := m.translationDeliveryAlreadyTerminal(persistCtx, job.ID)
			if terminalErr != nil {
				return terminalErr
			}
			if terminal {
				return nil
			}
		}
		return err
	}
	return mq.NewTerminalDeliveryError("translation_generation_failed", cause)
}

// translationJobFailedLogFields builds the bounded translation.job.failed
// record. Response issue kinds also go on the delivery span already in ctx,
// because provider validation can run after the provider span has ended.
func translationJobFailedLogFields(ctx context.Context, job *model.TranslationJob, cause error, logCause error) []any {
	fields := []any{"domain", "translation", "event", "translation.job.failed", "reason", classifyTranslationFailure(cause), failurecause.Attr(logCause)}
	if job != nil {
		fields = append(fields, "job_id", job.ID, "operation_id", job.OperationID, "entity_type", job.EntityType, "entity_id", job.EntityID, "target_locale", job.TargetLocale)
	}
	if issues := translation.ProviderResponseIssueKinds(logCause); len(issues) > 0 {
		joined := strings.Join(issues, ",")
		fields = append(fields, "response_issues", joined)
		trace.SpanFromContext(ctx).SetAttributes(attribute.String("translation.response_issues", joined))
	}
	return fields
}

// translationDeliveryTransportStopped distinguishes cancellation of the
// worker delivery itself from a timeout returned by a provider-owned child
// operation. Only the former remains running for transport redelivery; a
// provider timeout is a terminal failed attempt.
func translationDeliveryTransportStopped(ctx context.Context, cause error) bool {
	if !errors.Is(cause, context.Canceled) && !errors.Is(cause, context.DeadlineExceeded) {
		return false
	}
	return ctx != nil && ctx.Err() != nil
}

func translationTerminalCleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx.Err() == nil {
		return ctx, func() {}
	}
	return context.WithTimeout(context.WithoutCancel(ctx), translationTerminalCleanupTimeout)
}
