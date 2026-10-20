package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"syscall"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

func TestNormalizingHandlerEnforcesKeysCorrelationAndDenylist(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	handler := NewNormalizingHandler(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	record := slog.NewRecord(time.Now(), slog.LevelError, "delivery failed", 0)
	record.AddAttrs(
		slog.String("commandId", "command-1"),
		slog.String("recipient", "person@example.com"),
		slog.String("object-key", "private/path"),
		slog.String("sourcePath", "/private/source"),
		slog.Any("error", errors.New("person@example.com: rejected")),
		slog.Group("providerData", slog.String("providerMessageId", "provider-1")),
	)
	spanContext := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1},
		SpanID:  trace.SpanID{2},
	})
	ctx := trace.ContextWithSpanContext(context.Background(), spanContext)

	if err := handler.Handle(ctx, record); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	var entry structured.Fields
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatalf("decode log entry: %v; output=%s", err, output.String())
	}
	for _, key := range []string{"recipient", "object_key", "source_path"} {
		if _, exists := entry[key]; exists {
			t.Fatalf("forbidden key %q was emitted: %#v", key, entry)
		}
	}
	if got := entry["command_id"]; got != "command-1" {
		t.Fatalf("command_id = %q", got)
	}
	require.Equal(t, "[redacted-email]: rejected", entry["error"])
	if got := entry["error_type"]; got != "error_string" {
		t.Fatalf("error_type = %q", got)
	}
	if got := entry["trace_id"]; got != spanContext.TraceID().String() {
		t.Fatalf("trace_id = %q", got)
	}
	if got := entry["span_id"]; got != spanContext.SpanID().String() {
		t.Fatalf("span_id = %q", got)
	}
	provider, ok := entry["provider_data"].(structured.Fields)
	if !ok || provider["provider_message_id"] != "provider-1" {
		t.Fatalf("provider_data = %#v", provider)
	}
}

func TestNormalizingHandlerInjectsRequestIDFromContext(t *testing.T) {
	t.Parallel()

	const requestID = "11111111-1111-4111-8111-111111111111"
	ctx := ContextWithPropagatedRequestID(
		context.Background(),
		requestID,
		SystemActor{ServiceName: "test"},
	)
	var output bytes.Buffer
	handler := NewNormalizingHandler(slog.NewJSONHandler(&output, nil))
	record := slog.NewRecord(time.Now(), slog.LevelInfo, "observed", 0)
	require.NoError(t, handler.Handle(ctx, record))

	var entry structured.Fields
	require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
	require.Equal(t, requestID, entry["request_id"])
}

func TestNormalizingHandlerNormalizesPreformattedAttrsAndGroups(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	handler := NewNormalizingHandler(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})).
		WithAttrs([]slog.Attr{slog.String("requestId", "request-1"), slog.String("token", "secret")}).
		WithGroup("queueDelivery")

	record := slog.NewRecord(time.Now(), slog.LevelInfo, "started", 0)
	record.AddAttrs(slog.String("messageId", "message-1"))
	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	var entry structured.Fields
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatalf("decode log entry: %v; output=%s", err, output.String())
	}
	if entry["request_id"] != "request-1" {
		t.Fatalf("request_id = %#v", entry["request_id"])
	}
	queueDelivery, ok := entry["queue_delivery"].(structured.Fields)
	if !ok || queueDelivery["message_id"] != "message-1" {
		t.Fatalf("queue_delivery = %#v", queueDelivery)
	}
	if _, exists := entry["token"]; exists {
		t.Fatalf("token was emitted: %#v", entry)
	}
}

func TestNormalizingHandlerDoesNotDuplicateEnvelopeFromWithAttrs(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	handler := NewNormalizingHandler(slog.NewJSONHandler(&output, nil)).WithAttrs([]slog.Attr{
		slog.String("domain", "queue"),
		slog.String("event", "queue.delivery.succeeded"),
		slog.String("outcome", "succeeded"),
	})
	record := slog.NewRecord(time.Now(), slog.LevelInfo, "started", 0)
	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if got := output.String(); bytes.Count([]byte(got), []byte(`"domain"`)) != 1 ||
		bytes.Count([]byte(got), []byte(`"event"`)) != 1 ||
		bytes.Count([]byte(got), []byte(`"outcome"`)) != 1 {
		t.Fatalf("envelope fields were duplicated: %s", got)
	}
}

func TestNormalizingHandlerPreservesBoundAndGroupedFailureCauses(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	logger := slog.New(NewNormalizingHandler(slog.NewJSONHandler(&output, nil))).
		With("error", errors.New("connect database: connection refused"), "token", "private-token").
		WithGroup("worker").With("error", errors.New("write checkpoint: disk full"))
	logger.Error("failed", slog.Group("operation", "err", errors.New("publish: broker unavailable")))
	entry := decodeSingleLogEntry(t, output.Bytes())
	require.Equal(t, "connect database: connection refused", entry["error"])
	worker := entry["worker"].(map[string]any)
	require.Equal(t, "write checkpoint: disk full", worker["error"])
	require.Equal(t, "publish: broker unavailable", worker["operation"].(map[string]any)["error"])
	require.NotContains(t, output.String(), "private-token")
	require.NotContains(t, output.String(), diagnosticCauseKey)
}

func TestNormalizingHandlerKeepsDiagnosticSyntax(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	logger := slog.New(NewNormalizingHandler(slog.NewJSONHandler(&output, nil)))
	logger.Error("render failed", "error", errors.New(`layout {header}: <img> references relation "site_settings": person@example.test at https://provider.example/render?token=private-token`))
	entry := decodeSingleLogEntry(t, output.Bytes())
	require.Contains(t, entry["error"], `layout {header}: <img> references relation "site_settings"`)
	require.Contains(t, entry["error"], "https://provider.example/render")
	require.NotContains(t, output.String(), "person@example.test")
	require.NotContains(t, output.String(), "private-token")
}

func TestNormalizingHandlerKeepsScalarListsAndDropsStructures(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	handler := NewNormalizingHandler(slog.NewJSONHandler(&output, nil))
	logger := slog.New(handler)
	many := make([]int, 52)
	logger.Info("loaded",
		"queues", []string{"og.generate", "translation.generate"},
		"part_numbers", many,
		"signal", syscall.SIGTERM,
		"config", struct{ Secret string }{Secret: "do-not-log"},
		"recipient", []string{"person@example.com"},
	)

	var line map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &line))
	require.Equal(t, "og.generate,translation.generate", line["queues"])
	require.Contains(t, line["part_numbers"], ",+2 more")
	require.Equal(t, "terminated", line["signal"])
	require.NotContains(t, line, "config")
	require.NotContains(t, output.String(), "do-not-log")
	// Converting a value never bypasses the key denylist.
	require.NotContains(t, output.String(), "person@example.com")
}
