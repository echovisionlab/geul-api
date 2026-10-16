package telemetry

import (
	"context"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// SystemMetadata returns common System record metadata from the active correlation context.
func SystemMetadata(ctx context.Context, occurredAt time.Time) sharedtelemetry.SystemMetadata {
	return sharedtelemetry.SystemMetadata{
		OccurredAt:  occurredAt.UTC(),
		Correlation: sharedtelemetry.CorrelationFromContext(ctx),
	}
}

// EmitSystem emits a valid shared System record through the active logger.
func EmitSystem(ctx context.Context, record sharedtelemetry.SystemRecord, buildErr error) error {
	if buildErr != nil {
		slog.ErrorContext(ctx, "Failed to build media system telemetry", failurecause.Attr(buildErr))
		return buildErr
	}
	err := sharedtelemetry.EmitSystem(ctx, slog.Default().Handler(), record)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to emit media system telemetry", failurecause.Attr(err))
	}
	return err
}
