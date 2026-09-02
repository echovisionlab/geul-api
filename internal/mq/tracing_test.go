package mq

import (
	"context"
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestTracingMiddlewareDoesNotRecordRawHandlerError(t *testing.T) {
	const (
		sensitiveMessage = "person@example.com: provider payload rejected"
		requestID        = "11111111-1111-4111-8111-111111111111"
	)
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previousProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		require.NoError(t, provider.Shutdown(context.Background()))
	})

	ctx, span := StartConsumerSpan(context.Background(), Message{
		Queue:   "mail",
		Headers: structured.Fields{requestIDHeader: requestID},
	})
	require.Equal(t, requestID, telemetry.RequestIDFromContext(ctx))
	err := errors.New(sensitiveMessage)
	RecordConsumerError(span, err)
	span.End()

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	require.Empty(t, spans[0].Status().Description)
	require.Empty(t, spans[0].Events(), "raw exception events must not be recorded")
	for _, attr := range spans[0].Attributes() {
		require.NotContains(t, attr.Value.String(), sensitiveMessage)
	}
	require.Contains(t, spans[0].Attributes(), attribute.String("request_id", requestID))
}
