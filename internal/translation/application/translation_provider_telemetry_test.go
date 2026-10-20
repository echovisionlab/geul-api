package application

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/llm"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestTranslationProviderSpanUsesOnlyBoundedAllowlistedAttributes(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previousProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		require.NoError(t, provider.Shutdown(context.Background()))
	})

	const sensitiveProviderError = "provider response contained person@example.test"
	_, err := traceTranslationProviderOperation(
		context.Background(),
		"download",
		"provider response contained person@example.test",
		func(context.Context) (struct{}, error) {
			return struct{}{}, errors.New(sensitiveProviderError)
		},
	)
	require.Error(t, err)

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	require.Equal(t, "translation.provider.download", spans[0].Name())
	keys := make(map[string]string, len(spans[0].Attributes()))
	for _, attr := range spans[0].Attributes() {
		keys[string(attr.Key)] = attr.Value.AsString()
		require.NotContains(t, attr.Value.AsString(), sensitiveProviderError)
	}
	require.Equal(t, map[string]string{
		"failure_reason": translationFailureInternal,
		"operation":      "download",
		"provider":       "unknown",
		"exception_type": "unknown",
	}, keys)
	require.Empty(t, spans[0].Events())
	require.Equal(t, translationFailureInternal, spans[0].Status().Description)
}

func TestTranslationProviderGenerateSpanUsesTypedLLMFailureDetails(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previousProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		require.NoError(t, provider.Shutdown(context.Background()))
	})

	const sensitiveProviderMessage = "Gemini response for person@example.test"
	_, err := traceTranslationProviderOperation(
		context.Background(),
		"generate",
		"gemini",
		func(context.Context) (struct{}, error) {
			return struct{}{}, llm.NewProviderFailure(llm.ProviderFailureDetails{
				Category:        llm.ProviderFailureRateLimited,
				HTTPStatusClass: "4xx",
				ExceptionType:   llm.ProviderExceptionGenAIAPI,
			}, errors.New(sensitiveProviderMessage))
		},
	)
	require.Error(t, err)

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	require.Equal(t, "translation.provider.generate", spans[0].Name())
	keys := make(map[string]string, len(spans[0].Attributes()))
	for _, attr := range spans[0].Attributes() {
		keys[string(attr.Key)] = attr.Value.AsString()
		require.NotContains(t, attr.Value.AsString(), sensitiveProviderMessage)
	}
	require.Equal(t, map[string]string{
		"provider":          "gemini",
		"operation":         "generate",
		"failure_reason":    translationFailureProviderRateLimited,
		"http.status_class": "4xx",
		"exception_type":    string(llm.ProviderExceptionGenAIAPI),
	}, keys)
	require.Empty(t, spans[0].Events())
	require.Equal(t, translationFailureProviderRateLimited, spans[0].Status().Description)
}

func TestGenerateValidatedTranslationEmitsProviderGenerateSpan(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previousProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		require.NoError(t, provider.Shutdown(context.Background()))
	})

	generator := &scriptedTranslationGenerator{
		providerName: "gemini",
		session: &scriptedTranslationGeneratorSession{initialErrors: []error{
			llm.NewProviderFailure(llm.ProviderFailureDetails{
				Category:        llm.ProviderFailureAuthentication,
				HTTPStatusClass: "4xx",
				ExceptionType:   llm.ProviderExceptionGenAIAPI,
			}, errors.New("Gemini response body must not enter a span")),
		}},
	}

	_, err := (&TranslationJobManager{}).generateValidatedTranslationWithGenerator(
		context.Background(),
		nil,
		translationJobProviderRequest(translation.XLIFFUnit{ID: "unit-1", Source: "Hello"}),
		generator,
	)
	require.Error(t, err)

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	require.Equal(t, "translation.provider.generate", spans[0].Name())
	attributes := make(map[string]string, len(spans[0].Attributes()))
	for _, attr := range spans[0].Attributes() {
		attributes[string(attr.Key)] = attr.Value.AsString()
	}
	require.Equal(t, "gemini", attributes["provider"])
	require.Equal(t, "generate", attributes["operation"])
	require.Equal(t, "4xx", attributes["http.status_class"])
	require.Equal(t, translationFailureProviderAuthentication, attributes["failure_reason"])
	require.Equal(t, string(llm.ProviderExceptionGenAIAPI), attributes["exception_type"])
	for _, value := range attributes {
		require.NotContains(t, value, "Gemini response body")
	}
}

func TestTranslationResponseIssuesReachSpansAndJobFailureLog(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previousProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		require.NoError(t, provider.Shutdown(context.Background()))
	})
	validationErr := translation.ResponseValidationError(translation.HardValidationResult{
		Issues: []translation.ResponseIssue{
			{Kind: "target_empty", ID: "unit-1"},
			{Kind: "inline_structure_changed", ID: "unit-2", Reason: "inline code identity or nesting changed"},
		},
	})

	_, err := traceTranslationProviderOperation(context.Background(), "generate", "gemini",
		func(context.Context) (struct{}, error) { return struct{}{}, validationErr })
	require.ErrorIs(t, err, errTranslationProviderResponseInvalid)
	ctx, deliverySpan := otel.Tracer("test").Start(context.Background(), "translation process")
	job := &model.TranslationJob{ID: "job-1", OperationID: "op-1", EntityType: "page", EntityID: "page-9", TargetLocale: "ko"}
	fields := translationJobFailedLogFields(ctx, job, pinTranslationProviderDocumentError(validationErr), validationErr)
	deliverySpan.End()

	// Read the fields the way slog does: key-value pairs mixed with slog.Attr values.
	record := slog.NewRecord(time.Time{}, slog.LevelError, "", 0)
	record.Add(fields...)
	logged := make(map[string]any, record.NumAttrs())
	record.Attrs(func(attr slog.Attr) bool {
		logged[attr.Key] = attr.Value.Any()
		return true
	})
	require.Equal(t, "page-9", logged["entity_id"])
	require.Equal(t, "inline_structure_changed,target_empty", logged["response_issues"])
	require.Equal(t, translationFailureProviderResponseInvalid, logged["reason"])

	spans := recorder.Ended()
	require.Len(t, spans, 2)
	for span, key := range map[sdktrace.ReadOnlySpan]string{spans[0]: "response_issues", spans[1]: "translation.response_issues"} {
		attributes := make(map[string]string, len(span.Attributes()))
		for _, attr := range span.Attributes() {
			attributes[string(attr.Key)] = attr.Value.AsString()
		}
		require.Equal(t, "inline_structure_changed,target_empty", attributes[key], span.Name())
	}
}
