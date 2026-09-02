package telemetry

import (
	"log/slog"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// NewFanoutHandler creates an slog.Handler that fans out records to all
// provided handlers. This allows logs to go to both stdout and OTel
// simultaneously.
func NewFanoutHandler(handlers ...slog.Handler) slog.Handler {
	return sharedtelemetry.NewFanoutHandler(handlers...)
}
