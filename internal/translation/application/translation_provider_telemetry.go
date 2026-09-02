package application

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/llm"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// traceTranslationProviderOperation records only bounded operation and failure
// attributes. Provider response bodies and arbitrary error strings never enter
// traces through this edge.
func traceTranslationProviderOperation[T any](
	ctx context.Context,
	operation string,
	provider string,
	call func(context.Context) (T, error),
) (T, error) {
	boundedOperation := boundedTranslationProviderOperation(operation)
	ctx, span := otel.Tracer(
		sharedtelemetry.ServiceBackend.Instrumentation("translation"),
	).Start(
		ctx,
		"translation.provider."+boundedOperation,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("provider", boundedTranslationProviderName(provider)),
			attribute.String("operation", boundedOperation),
		),
	)
	defer span.End()

	result, err := call(ctx)
	if err != nil {
		reason := classifyTranslationFailure(err)
		span.SetStatus(codes.Error, reason)
		attributes := []attribute.KeyValue{
			attribute.String("failure_reason", reason),
			attribute.String("exception_type", translationProviderExceptionType(err)),
		}
		if details, ok := llm.ProviderFailureDetailsFromError(err); ok && details.HTTPStatusClass != "" {
			attributes = append(attributes, attribute.String("http.status_class", details.HTTPStatusClass))
		}
		span.SetAttributes(attributes...)
	}
	return result, err
}

func boundedTranslationProviderName(provider string) string {
	switch provider {
	case "deepl", "gemini", "openai-compatible":
		return provider
	default:
		return "unknown"
	}
}

func boundedTranslationProviderOperation(operation string) string {
	switch operation {
	case "upload", "poll", "download", "generate":
		return operation
	default:
		return "unknown"
	}
}

func translationProviderExceptionType(err error) string {
	if details, ok := llm.ProviderFailureDetailsFromError(err); ok {
		return string(details.ExceptionType)
	}
	return string(llm.ProviderExceptionUnknown)
}
