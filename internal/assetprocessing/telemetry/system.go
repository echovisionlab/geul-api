package telemetry

import (
	"context"
	"log/slog"
	"time"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func SystemMetadata(ctx context.Context, occurredAt time.Time) sharedtelemetry.SystemMetadata {
	return sharedtelemetry.SystemMetadata{
		OccurredAt:  occurredAt.UTC(),
		Correlation: sharedtelemetry.CorrelationFromContext(ctx),
	}
}

func EmitSystem(ctx context.Context, record sharedtelemetry.SystemRecord, buildErr error) error {
	if buildErr != nil {
		return buildErr
	}
	return sharedtelemetry.EmitSystem(ctx, slog.Default().Handler(), record)
}
