package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/structured"
)

func TestBuildGenerationConfig(t *testing.T) {
	t.Run("structured response uses JSON MIME type and lower temperature", func(t *testing.T) {
		config := buildGenerationConfig(GenerationRequest{
			SystemPrompt: "system prompt",
			ResponseJSONSchema: structured.Fields{
				"type": "object",
			},
		}, nil)

		require.NotNil(t, config)
		require.NotNil(t, config.Temperature)
		assert.Equal(t, float32(0.2), *config.Temperature)
		assert.Equal(t, "application/json", config.ResponseMIMEType)
		assert.Equal(t, structured.Fields{"type": "object"}, config.ResponseJsonSchema)
	})

	t.Run("ordinary text uses the default temperature", func(t *testing.T) {
		config := buildGenerationConfig(GenerationRequest{SystemPrompt: "system prompt"}, nil)

		require.NotNil(t, config)
		require.NotNil(t, config.Temperature)
		assert.Equal(t, float32(0.7), *config.Temperature)
		assert.Empty(t, config.ResponseMIMEType)
		assert.Nil(t, config.ResponseJsonSchema)
	})
}

func TestRequestTimeout(t *testing.T) {
	assert.Equal(t, 30*time.Second, requestTimeout(0))
	assert.Equal(t, 5*time.Second, requestTimeout(5*time.Second))
}

func TestOpenAICompatibleProviderUsesChatCompletions(t *testing.T) {
	t.Parallel()

	var captured openAICompatibleChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/chat/completions", r.URL.Path)
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&captured))

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"summary\":\"ok\"}"}}]}`))
	}))
	defer server.Close()

	provider, err := NewOpenAICompatibleProvider(OpenAICompatibleConfig{
		APIKey: "test-key", BaseURL: server.URL, Model: "custom-model", SupportsJSONMode: true,
		MaxOutputTokens: new(int32(512)),
	})
	require.NoError(t, err)

	text, err := provider.GenerateText(context.Background(), GenerationRequest{
		RequestID:    "req-1",
		Action:       "structured-response",
		SystemPrompt: "Return JSON.",
		UserPrompt:   `{"task":"summary"}`,
		ResponseJSONSchema: structured.Fields{
			"type": "object",
		},
		ResponseSchemaName: "response-schema",
		Timeout:            5 * time.Second,
	})
	require.NoError(t, err)

	assert.JSONEq(t, `{"summary":"ok"}`, text)
	assert.Equal(t, "custom-model", captured.Model)
	assert.Equal(t, int32(512), *captured.MaxTokens)
	require.NotNil(t, captured.ResponseFormat)
	assert.Equal(t, "json_schema", captured.ResponseFormat["type"])
	schema, ok := captured.ResponseFormat["json_schema"].(structured.Fields)
	require.True(t, ok)
	assert.Equal(t, "response-schema", schema["name"])
	require.Len(t, captured.Messages, 2)
	assert.Equal(t, "system", captured.Messages[0].Role)
	assert.Equal(t, "user", captured.Messages[1].Role)
}

func TestOpenAICompatibleProviderRedactsProviderResponseBody(t *testing.T) {
	const sensitiveResponse = "provider response contains person@example.test"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(sensitiveResponse))
	}))
	defer server.Close()

	provider, err := NewOpenAICompatibleProvider(OpenAICompatibleConfig{
		APIKey: "test-key", BaseURL: server.URL, Model: "custom-model",
	})
	require.NoError(t, err)

	_, err = provider.GenerateText(context.Background(), GenerationRequest{
		RequestID: "req-1", SystemPrompt: "system", UserPrompt: "source text",
	})
	require.Error(t, err)
	require.NotContains(t, err.Error(), sensitiveResponse)
	details, ok := ProviderFailureDetailsFromError(err)
	require.True(t, ok)
	require.Equal(t, ProviderFailureRateLimited, details.Category)
	require.Equal(t, "4xx", details.HTTPStatusClass)
	require.Equal(t, ProviderExceptionProviderResponse, details.ExceptionType)
}

func TestOpenAICompatibleProviderRedactsSuccessfulHTTPErrorEnvelope(t *testing.T) {
	const sensitiveMessage = "provider error message for person@example.test"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":{"message":"` + sensitiveMessage + `"}}`))
	}))
	defer server.Close()

	provider, err := NewOpenAICompatibleProvider(OpenAICompatibleConfig{
		APIKey: "test-key", BaseURL: server.URL, Model: "custom-model",
	})
	require.NoError(t, err)

	_, err = provider.GenerateText(context.Background(), GenerationRequest{
		RequestID: "req-1", SystemPrompt: "system", UserPrompt: "source text",
	})
	require.Error(t, err)
	require.NotContains(t, err.Error(), sensitiveMessage)
	details, ok := ProviderFailureDetailsFromError(err)
	require.True(t, ok)
	require.Equal(t, ProviderFailureRejected, details.Category)
	require.Empty(t, details.HTTPStatusClass)
	require.Equal(t, ProviderExceptionProviderResponse, details.ExceptionType)
}

func TestNormalizeOpenAICompatibleChatURL(t *testing.T) {
	t.Parallel()

	_, err := normalizeOpenAICompatibleChatURL("not-a-url")
	require.ErrorContains(t, err, "invalid OpenAI-compatible API base URL")

	url, err := normalizeOpenAICompatibleChatURL("https://llm.example.com/custom/chat/completions")
	require.NoError(t, err)
	assert.Equal(t, "https://llm.example.com/custom/chat/completions", url)
}
