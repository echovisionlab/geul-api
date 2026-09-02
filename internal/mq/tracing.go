package mq

import (
	"context"
	"reflect"

	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/telemetry"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const requestIDHeader = "x-request-id"

func InjectTraceContext(ctx context.Context) map[string]string {
	headers := map[string]string{}
	otel.GetTextMapPropagator().Inject(ctx, propagation.MapCarrier(headers))
	if requestID := telemetry.RequestIDFromContext(ctx); requestID != "" {
		headers[requestIDHeader] = requestID
	}
	return headers
}

func StartConsumerSpan(ctx context.Context, msg Message) (context.Context, trace.Span) {
	if msg.Headers != nil {
		carrier := propagation.MapCarrier{}
		for key, value := range msg.Headers {
			if text, ok := value.(string); ok {
				carrier[key] = text
			}
		}
		ctx = otel.GetTextMapPropagator().Extract(ctx, carrier)
		if requestID, ok := msg.Headers[requestIDHeader].(string); ok {
			ctx = telemetry.ContextWithPropagatedRequestID(
				ctx,
				requestID,
				telemetry.SystemActor{ServiceName: sharedtelemetry.ServiceBackend},
			)
		}
	}
	attrs := []attribute.KeyValue{
		attribute.String("messaging.system", "pgmq"),
		attribute.String("messaging.destination.name", msg.Queue),
		attribute.String("messaging.operation.type", "process"),
	}
	if requestID := telemetry.RequestIDFromContext(ctx); requestID != "" {
		attrs = append(attrs, attribute.String("request_id", requestID))
	}
	return otel.Tracer(sharedtelemetry.ServiceBackend.Instrumentation("mq")).Start(
		ctx,
		msg.Queue+" process",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(attrs...),
	)
}

func stringHeaders(headers map[string]string) structured.Fields {
	if len(headers) == 0 {
		return nil
	}
	fields := make(structured.Fields, len(headers))
	for key, value := range headers {
		fields[key] = value
	}
	return fields
}

func RecordConsumerError(span trace.Span, err error) {
	if err == nil {
		return
	}
	span.SetAttributes(attribute.String("error.type", reflect.TypeOf(err).String()))
	span.SetStatus(codes.Error, "")
}
