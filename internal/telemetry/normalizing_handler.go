package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"strings"

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
		} else if !h.restore && attr.Value.Kind() == slog.KindAny {
			// The shared handler drops every KindAny value. Keep lists of scalars and
			// scalar Stringers (os.Signal) as text; structs and maps stay dropped.
			if text, ok := scalarText(attr.Value.Any()); ok {
				attr.Value = slog.StringValue(text)
			}
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

const (
	maxLoggedListItems = 50
	maxLoggedListText  = 1024
)

// scalarText renders a slice or array of strings, booleans or numbers as a
// comma-separated list (capped), and a non-struct fmt.Stringer as its String.
func scalarText(value any) (string, bool) {
	if value == nil {
		return "", false
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Slice, reflect.Array:
		if !isScalarKind(reflected.Type().Elem().Kind()) {
			return "", false
		}
		items := make([]string, 0, min(reflected.Len(), maxLoggedListItems))
		for i := 0; i < reflected.Len() && i < maxLoggedListItems; i++ {
			items = append(items, fmt.Sprint(reflected.Index(i).Interface()))
		}
		text := strings.Join(items, ",")
		if hidden := reflected.Len() - len(items); hidden > 0 {
			text += fmt.Sprintf(",+%d more", hidden)
		}
		if runes := []rune(text); len(runes) > maxLoggedListText {
			text = string(runes[:maxLoggedListText]) + "…"
		}
		return text, true
	}
	if stringer, ok := value.(fmt.Stringer); ok && isScalarKind(reflected.Kind()) {
		return stringer.String(), true
	}
	return "", false
}

func isScalarKind(kind reflect.Kind) bool {
	switch kind {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}
