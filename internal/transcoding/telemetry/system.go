package telemetry

import (
	"context"
	"log/slog"
	"time"

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
		return buildErr
	}
	return sharedtelemetry.EmitSystem(ctx, slog.Default().Handler(), record)
}
