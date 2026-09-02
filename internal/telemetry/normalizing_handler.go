package telemetry

import (
	"log/slog"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// NewNormalizingHandler applies the shared key, redaction, and correlation
// contract. Ordinary diagnostic logs deliberately do not invent a catalog
// event or outcome.
func NewNormalizingHandler(next slog.Handler) slog.Handler {
	return sharedtelemetry.NewNormalizingHandler(next)
}
