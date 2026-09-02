package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/llm"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"
)

func TestMetadataAIProviderObserverRedactsProviderErrorDetails(t *testing.T) {
	var output bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	const sensitiveBody = "provider response for person@example.test"
	observer := metadataAIProviderObserver{}
	observer.RequestFailed(context.Background(), llm.RequestFailedEvent{
		Request:    llm.GenerationRequest{RequestID: "request-1", Action: "translation-json"},
		StartedAt:  time.Now().Add(-time.Second),
		ContextErr: context.DeadlineExceeded,
		Err: llm.NewProviderFailure(llm.ProviderFailureDetails{
			Category:        llm.ProviderFailureRateLimited,
			HTTPStatusClass: "4xx",
			ExceptionType:   llm.ProviderExceptionGenAIAPI,
		}, genai.APIError{Code: 429, Status: "RESOURCE_EXHAUSTED", Message: sensitiveBody}),
	})

	line := output.String()
	require.NotContains(t, line, sensitiveBody)
	require.NotContains(t, line, "RESOURCE_EXHAUSTED")
	require.NotContains(t, line, "api_message")
	require.NotContains(t, line, "api_status")
	require.NotContains(t, line, `"error"`)

	var record map[string]any
	require.NoError(t, json.Unmarshal([]byte(line), &record))
	require.Equal(t, "AI provider request failed", record["msg"])
	require.Equal(t, "deadline_exceeded", record["context_error"])
	require.Equal(t, string(llm.ProviderFailureRateLimited), record["provider_failure_category"])
	require.Equal(t, "4xx", record["http_status_class"])
	require.Equal(t, string(llm.ProviderExceptionGenAIAPI), record["exception_type"])
}

func TestBoundedContextError(t *testing.T) {
	require.Equal(t, "", boundedContextError(nil))
	require.Equal(t, "canceled", boundedContextError(context.Canceled))
	require.Equal(t, "deadline_exceeded", boundedContextError(context.DeadlineExceeded))
	require.Equal(t, "unknown", boundedContextError(genai.APIError{Message: "provider body"}))
}
