package telemetry

import (
	"context"

	"connectrpc.com/connect"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.Tracer(sharedtelemetry.ServiceBackend.Instrumentation("connect"))

// TracingInterceptor adds distributed tracing spans to Connect RPC calls.
type TracingInterceptor struct{}

// NewTracingInterceptor creates a new tracing interceptor for Connect RPC.
func NewTracingInterceptor() *TracingInterceptor {
	return &TracingInterceptor{}
}

// WrapUnary wraps a unary Connect RPC call with a tracing span.
func (i *TracingInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		procedure := req.Spec().Procedure

		attrs := []attribute.KeyValue{
			attribute.String("rpc.system", "connect"),
			attribute.String("rpc.procedure", procedure),
		}
		if requestID := RequestIDFromContext(ctx); requestID != "" {
			attrs = append(attrs, attribute.String("request_id", requestID))
		}
		ctx, span := tracer.Start(ctx, procedure,
			trace.WithSpanKind(trace.SpanKindInternal),
			trace.WithAttributes(attrs...),
		)
		defer span.End()

		resp, err := next(ctx, req)
		if err != nil {
			code := connect.CodeOf(err).String()
			span.SetAttributes(attribute.String("rpc.connect.status_code", code))
			span.SetStatus(codes.Error, code)
		}

		return resp, err
	}
}

// WrapStreamingClient is a no-op for server-side interceptors.
func (i *TracingInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler wraps a streaming Connect RPC call with a tracing span.
func (i *TracingInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		procedure := conn.Spec().Procedure

		attrs := []attribute.KeyValue{
			attribute.String("rpc.system", "connect"),
			attribute.String("rpc.procedure", procedure),
		}
		if requestID := RequestIDFromContext(ctx); requestID != "" {
			attrs = append(attrs, attribute.String("request_id", requestID))
		}
		ctx, span := tracer.Start(ctx, procedure,
			trace.WithSpanKind(trace.SpanKindInternal),
			trace.WithAttributes(attrs...),
		)
		defer span.End()

		err := next(ctx, conn)
		if err != nil {
			code := connect.CodeOf(err).String()
			span.SetAttributes(attribute.String("rpc.connect.status_code", code))
			span.SetStatus(codes.Error, code)
		}

		return err
	}
}
