package telemetry

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestFanoutHandlerEnabledWhenAnyChildHandlerEnabled(t *testing.T) {
	infoHandler := slog.NewJSONHandler(&bytes.Buffer{}, nil)
	errorHandler := slog.NewJSONHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError})
	handler := NewFanoutHandler(errorHandler, infoHandler)

	if !handler.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("expected info level to be enabled by one child handler")
	}
	if handler.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("expected debug level to be disabled by all child handlers")
	}
}

func TestFanoutHandlerWritesOnlyEnabledChildren(t *testing.T) {
	var warnOnly bytes.Buffer
	var info bytes.Buffer
	handler := NewFanoutHandler(
		slog.NewJSONHandler(&warnOnly, &slog.HandlerOptions{Level: slog.LevelWarn}),
		slog.NewJSONHandler(&info, nil),
	)

	record := slog.NewRecord(time.Unix(1, 0), slog.LevelInfo, "fanout", 0)
	record.AddAttrs(slog.String("entity", "post"))
	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle() returned error: %v", err)
	}

	if warnOnly.Len() != 0 {
		t.Fatalf("warn-only handler received disabled info record: %s", warnOnly.String())
	}
	if got := info.String(); !strings.Contains(got, `"msg":"fanout"`) || !strings.Contains(got, `"entity":"post"`) {
		t.Fatalf("info handler did not receive expected record: %s", got)
	}
}

func TestFanoutHandlerWithAttrsAndGroup(t *testing.T) {
	var first bytes.Buffer
	var second bytes.Buffer
	handler := NewFanoutHandler(
		slog.NewJSONHandler(&first, nil),
		slog.NewJSONHandler(&second, nil),
	).WithAttrs([]slog.Attr{slog.String("service", "backend")}).
		WithGroup("audit")

	record := slog.NewRecord(time.Unix(1, 0), slog.LevelInfo, "event", 0)
	record.AddAttrs(slog.String("action", "publish"))
	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle() returned error: %v", err)
	}

	for _, got := range []string{first.String(), second.String()} {
		if !strings.Contains(got, `"service":"backend"`) || !strings.Contains(got, `"audit":{"action":"publish"}`) {
			t.Fatalf("fanout child missing attrs/group: %s", got)
		}
	}
}
