package telemetry

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestTracingInterceptorRecordsOnlyBoundedConnectError(t *testing.T) {
	const sensitiveMessage = "person@example.com: authorization failed"
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previousTracer := tracer
	tracer = provider.Tracer("test/connect")
	t.Cleanup(func() {
		tracer = previousTracer
		require.NoError(t, provider.Shutdown(context.Background()))
	})

	wrapped := NewTracingInterceptor().WrapUnary(func(
		context.Context,
		connect.AnyRequest,
	) (connect.AnyResponse, error) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New(sensitiveMessage))
	})

	_, err := wrapped(context.Background(), connect.NewRequest(&emptypb.Empty{}))
	require.Error(t, err)

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	require.Equal(t, "permission_denied", spans[0].Status().Description)
	require.Empty(t, spans[0].Events(), "raw exception events must not be recorded")
	for _, attr := range spans[0].Attributes() {
		require.NotContains(t, attr.Value.String(), sensitiveMessage)
	}
}
