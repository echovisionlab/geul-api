package llm

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/genai"
)

func TestGeminiProviderFailureClassifiesSDKHTTPStatusWithoutLeakingMessage(t *testing.T) {
	const sensitiveMessage = "raw Gemini response for person@example.test"

	tests := []struct {
		name     string
		status   int
		category ProviderFailureCategory
		class    string
	}{
		{name: "authentication", status: http.StatusUnauthorized, category: ProviderFailureAuthentication, class: "4xx"},
		{name: "rate limited", status: http.StatusTooManyRequests, category: ProviderFailureRateLimited, class: "4xx"},
		{name: "unavailable", status: http.StatusServiceUnavailable, category: ProviderFailureUnavailable, class: "5xx"},
		{name: "rejected", status: http.StatusBadRequest, category: ProviderFailureRejected, class: "4xx"},
		{name: "missing model is configuration", status: http.StatusNotFound, category: ProviderFailureConfiguration, class: "4xx"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := geminiProviderFailure(genai.APIError{Code: test.status, Message: sensitiveMessage}, nil)
			details, ok := ProviderFailureDetailsFromError(err)
			require.True(t, ok)
			require.Equal(t, test.category, details.Category)
			require.Equal(t, test.class, details.HTTPStatusClass)
			require.Equal(t, ProviderExceptionGenAIAPI, details.ExceptionType)
			require.NotContains(t, err.Error(), sensitiveMessage)
		})
	}
}

func TestProviderFailureDetailsAreBounded(t *testing.T) {
	err := NewProviderFailure(ProviderFailureDetails{
		Category:        ProviderFailureCategory("provider supplied category"),
		HTTPStatusClass: "418",
		ExceptionType:   ProviderExceptionType("provider supplied exception"),
	}, errors.New("provider response body: secret"))

	details, ok := ProviderFailureDetailsFromError(err)
	require.True(t, ok)
	require.Equal(t, ProviderFailureInternal, details.Category)
	require.Empty(t, details.HTTPStatusClass)
	require.Equal(t, ProviderExceptionUnknown, details.ExceptionType)
	require.NotContains(t, err.Error(), "secret")
}

func TestGeminiProviderFailurePreservesCallerCancellation(t *testing.T) {
	err := geminiProviderFailure(context.Canceled, nil)
	require.ErrorIs(t, err, context.Canceled)
	_, ok := ProviderFailureDetailsFromError(err)
	require.False(t, ok)
}
