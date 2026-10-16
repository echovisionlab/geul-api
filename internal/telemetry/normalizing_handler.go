package telemetry

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// NewNormalizingHandler keeps the shared key, denylist, and correlation rules,
// while retaining a sanitized cause alongside the shared error_type. The shared
// handler otherwise replaces every error with its Go type, hiding diagnoses.
func NewNormalizingHandler(next slog.Handler) slog.Handler {
	return &diagnosticHandler{next: sharedtelemetry.NewNormalizingHandler(&diagnosticHandler{next: next, restore: true})}
}

// Carry the sanitized cause through normalization under a private key, then
// restore the conventional error key at the output boundary. WithAttrs and
// WithGroup must follow the same path as attributes attached to a record.
type diagnosticHandler struct {
	next    slog.Handler
	restore bool
}

const diagnosticCauseKey = "diagnostic_error_cause"

func (h *diagnosticHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *diagnosticHandler) Handle(ctx context.Context, record slog.Record) error {
	mapped := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)
	attrs := make([]slog.Attr, 0, record.NumAttrs())
	record.Attrs(func(attr slog.Attr) bool {
		attrs = append(attrs, attr)
		return true
	})
	mapped.AddAttrs(h.attrs(attrs)...)
	return h.next.Handle(ctx, mapped)
}

func (h *diagnosticHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &diagnosticHandler{next: h.next.WithAttrs(h.attrs(attrs)), restore: h.restore}
}

func (h *diagnosticHandler) WithGroup(name string) slog.Handler {
	return &diagnosticHandler{next: h.next.WithGroup(name), restore: h.restore}
}

func (h *diagnosticHandler) attrs(attrs []slog.Attr) []slog.Attr {
	mapped := make([]slog.Attr, 0, len(attrs))
	for _, attr := range attrs {
		attr.Value = attr.Value.Resolve()
		key := sharedtelemetry.NormalizeKey(attr.Key)
		if h.restore && key == diagnosticCauseKey {
			attr.Key = "error"
		} else if !h.restore && (key == "error" || key == "err") {
			var cause error
			if value := attr.Value.Any(); value != nil {
				cause, _ = value.(error)
				if cause == nil {
					cause = fmt.Errorf("%v", value)
				}
			}
			mapped = append(mapped, slog.String(diagnosticCauseKey, failurecause.Message(cause)))
		} else if !h.restore && key == diagnosticCauseKey {
			// Do not allow a caller to bypass sanitization using the carrier key.
			continue
		} else if attr.Value.Kind() == slog.KindGroup {
			attr.Value = slog.GroupValue(h.attrs(attr.Value.Group())...)
		}
		if h.restore && attr.Value.Kind() == slog.KindGroup {
			// Shared normalization may emit bound and record attributes in
			// separate groups with the same key. Merge them before JSON/OTLP
			// serialization so the later group cannot erase the bound cause.
			merged := false
			for i, previous := range mapped {
				if previous.Key == attr.Key && previous.Value.Kind() == slog.KindGroup {
					children := append([]slog.Attr{}, previous.Value.Group()...)
					children = append(children, attr.Value.Group()...)
					mapped[i].Value = slog.GroupValue(h.attrs(children)...)
					merged = true
					break
				}
			}
			if merged {
				continue
			}
		}
		mapped = append(mapped, attr)
	}
	return mapped
}
