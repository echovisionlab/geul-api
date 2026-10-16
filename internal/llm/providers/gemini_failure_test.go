package providers

import (
	"context"
	"net/http"
	"testing"

	"github.com/echovisionlab/geul-api/internal/llm"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"
)

func TestGeminiProviderFailureClassifiesSDKHTTPStatusWithoutLeakingMessage(t *testing.T) {
	const sensitiveMessage = "raw Gemini response for person@example.test"

	tests := []struct {
		name     string
		status   int
		category llm.ProviderFailureCategory
		class    string
	}{
		{name: "authentication", status: http.StatusUnauthorized, category: llm.ProviderFailureAuthentication, class: "4xx"},
		{name: "rate limited", status: http.StatusTooManyRequests, category: llm.ProviderFailureRateLimited, class: "4xx"},
		{name: "unavailable", status: http.StatusServiceUnavailable, category: llm.ProviderFailureUnavailable, class: "5xx"},
		{name: "rejected", status: http.StatusBadRequest, category: llm.ProviderFailureRejected, class: "4xx"},
		{name: "missing model is configuration", status: http.StatusNotFound, category: llm.ProviderFailureConfiguration, class: "4xx"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := geminiProviderFailure(genai.APIError{Code: test.status, Message: sensitiveMessage}, nil)
			details, ok := llm.ProviderFailureDetailsFromError(err)
			require.True(t, ok)
			require.Equal(t, test.category, details.Category)
			require.Equal(t, test.class, details.HTTPStatusClass)
			require.Equal(t, llm.ProviderExceptionGenAIAPI, details.ExceptionType)
			require.NotContains(t, err.Error(), sensitiveMessage)
		})
	}
}

func TestGeminiProviderFailurePreservesCallerCancellation(t *testing.T) {
	err := geminiProviderFailure(context.Canceled, nil)
	require.ErrorIs(t, err, context.Canceled)
	_, ok := llm.ProviderFailureDetailsFromError(err)
	require.False(t, ok)
}
