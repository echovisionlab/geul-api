package application

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

func TestTranslationTerminalTelemetryPreservesRequestTraceAndSpanCorrelation(t *testing.T) {
	request, err := sharedtelemetry.NewPropagatedRequestContext(uuid.NewString(), sharedtelemetry.AnonymousActor{})
	require.NoError(t, err)
	traceID, err := trace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	require.NoError(t, err)
	spanID, err := trace.SpanIDFromHex("0123456789abcdef")
	require.NoError(t, err)
	ctx := trace.ContextWithSpanContext(
		sharedtelemetry.WithRequestContext(context.Background(), request),
		trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled}),
	)
	job := &Job{ID: uuid.NewString(), EntityType: "post", EntityID: uuid.NewString(), TargetLocale: "en"}
	endedAt := time.Unix(1_700_001_600, 0).UTC()
	events := captureTranslationTerminalEvents(t, func() {
		emitTranslationJobTerminal(ctx, job, translationJobTerminalOutcomeApplied, "", endedAt.Add(-time.Second), endedAt)
	})

	require.Len(t, events, 1)
	require.Equal(t, request.RequestID, events[0]["request_id"])
	require.Equal(t, traceID.String(), events[0]["trace_id"])
	require.Equal(t, spanID.String(), events[0]["span_id"])
}

func captureTranslationTerminalEvents(t *testing.T, run func()) []map[string]any {
	t.Helper()
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	run()

	decoder := json.NewDecoder(&output)
	var events []map[string]any
	for decoder.More() {
		var event map[string]any
		require.NoError(t, decoder.Decode(&event))
		if event["event"] == "translation.job.terminal" {
			events = append(events, event)
		}
	}
	return events
}
