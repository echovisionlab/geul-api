package application

import (
	"context"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

type translationJobTerminalOutcome = sharedtelemetry.TranslationJobTerminalOutcome

const (
	translationJobTerminalOutcomeApplied   = sharedtelemetry.TranslationJobTerminalOutcomeApplied
	translationJobTerminalOutcomeFailed    = sharedtelemetry.TranslationJobTerminalOutcomeFailed
	translationJobTerminalOutcomeCancelled = sharedtelemetry.TranslationJobTerminalOutcomeCancelled
)

// emitTranslationJobTerminal is the only terminal Translation signal. The
// transport row and its artifacts have already been deleted when this runs, so
// the record contains only bounded operational context and no document body,
// entity identity, provider, or model.
func emitTranslationJobTerminal(
	ctx context.Context,
	job *model.TranslationJob,
	outcome translationJobTerminalOutcome,
	errorClassification string,
	startedAt time.Time,
	endedAt time.Time,
) {
	if job == nil || job.ID == "" {
		return
	}
	durationMS := endedAt.Sub(startedAt).Milliseconds()
	if startedAt.IsZero() || durationMS < 0 {
		durationMS = 0
	}
	record, err := sharedtelemetry.NewTranslationJobTerminalRecord(
		sharedtelemetry.SystemMetadata{
			OccurredAt:  endedAt.UTC(),
			Correlation: sharedtelemetry.CorrelationFromContext(ctx),
		},
		sharedtelemetry.TranslationJobTerminalContext{
			JobID:        job.ID,
			EntityType:   sharedtelemetry.TranslationEntityType(job.EntityType),
			TargetLocale: job.TargetLocale,
			DurationMS:   durationMS,
		},
		outcome,
		sharedtelemetry.TranslationFailureReason(errorClassification),
	)
	if err != nil {
		slog.ErrorContext(ctx, "Translation terminal telemetry rejected",
			"job_id", job.ID,
			"outcome", outcome,
			"error_classification", errorClassification,
			"error", err,
		)
		return
	}
	if err := sharedtelemetry.EmitSystem(ctx, slog.Default().Handler(), record); err != nil {
		slog.ErrorContext(ctx, "Translation terminal telemetry emission failed",
			"job_id", job.ID,
			"outcome", outcome,
			"error", err,
		)
	}
}
