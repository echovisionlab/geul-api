package providers

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/echovisionlab/geul-api/internal/llm"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"
)

func captureFailureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &output
}

func TestOpenAICompatibleFailureLogsExactStatusAndRequestContext(t *testing.T) {
	output := captureFailureLogs(t)
	provider, err := NewOpenAICompatibleProvider(OpenAICompatibleConfig{APIKey: "private-token", BaseURL: "https://provider.test", Model: "test"})
	require.NoError(t, err)
	provider.(*openAICompatibleTextProvider).httpClient.Transport = failureTestTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"private response body"}}`))}, nil
	})
	_, err = provider.GenerateText(t.Context(), llm.GenerationRequest{RequestID: "job-1", Action: "metadata-json", UserPrompt: "private prompt"})
	require.Error(t, err)
	var record map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &record))
	require.EqualValues(t, 401, record["http_status"])
	require.Equal(t, "job-1", record["request_id"])
	require.Equal(t, "metadata-json", record["action"])
	require.Equal(t, "single", record["turn_kind"])
	require.Equal(t, "openai-compatible", record["provider"])
	require.NotEmpty(t, record["failure_cause"])
	for _, secret := range []string{"private-token", "private response body", "private prompt"} {
		require.NotContains(t, output.String(), secret)
	}
}

type failureTestTransport func(*http.Request) (*http.Response, error)

func (f failureTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOpenAICompatibleLogsDiagnosticCauseWithPrivateValuesRedacted(t *testing.T) {
	for _, source := range []string{"transport", "provider"} {
		t.Run(source, func(t *testing.T) {
			output := captureFailureLogs(t)
			provider, err := NewOpenAICompatibleProvider(OpenAICompatibleConfig{APIKey: "private-token", BaseURL: "https://provider.test", Model: "test"})
			require.NoError(t, err)
			const diagnosis = `failed at "translation_jobs" <unit> {cause}: connection refused`
			provider.(*openAICompatibleTextProvider).httpClient.Transport = failureTestTransport(func(*http.Request) (*http.Response, error) {
				message := diagnosis + " private-token private@example.test https://user:password@provider.test/path?key=private-query"
				if source == "transport" {
					return nil, errors.New(message)
				}
				body, err := json.Marshal(map[string]any{"error": map[string]string{"message": message}})
				require.NoError(t, err)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body))}, nil
			})
			_, err = provider.GenerateText(t.Context(), llm.GenerationRequest{RequestID: "job-2", Action: "translation-json"})
			require.Error(t, err)
			var record map[string]any
			require.NoError(t, json.Unmarshal(output.Bytes(), &record))
			require.Contains(t, record["failure_cause"], diagnosis)
			for _, secret := range []string{"private-token", "private@example.test", "user:password", "private-query"} {
				require.NotContains(t, output.String(), secret)
			}
		})
	}
}

func TestOpenAICompatibleRedactsEarlierSessionPrompts(t *testing.T) {
	output := captureFailureLogs(t)
	provider, err := NewOpenAICompatibleProvider(OpenAICompatibleConfig{APIKey: "private-key", BaseURL: "https://provider.test", Model: "test"})
	require.NoError(t, err)
	calls := 0
	provider.(*openAICompatibleTextProvider).httpClient.Transport = failureTestTransport(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"private prior response"}}]}`))}, nil
		}
		return nil, errors.New("connection refused: private initial prompt; private prior response")
	})
	session, err := provider.StartSession(t.Context(), llm.SessionSpec{RequestID: "job-history"})
	require.NoError(t, err)
	_, err = session.GenerateText(t.Context(), llm.SessionTurn{UserPrompt: "private initial prompt"})
	require.NoError(t, err)
	_, err = session.GenerateText(t.Context(), llm.SessionTurn{UserPrompt: "next turn"})
	require.Error(t, err)
	require.Contains(t, output.String(), "connection refused")
	require.NotContains(t, output.String(), "private initial prompt")
	require.NotContains(t, output.String(), "private prior response")
}

func TestGeminiLogsAPIMessageWithoutResponseDetails(t *testing.T) {
	output := captureFailureLogs(t)
	client, err := genai.NewClient(t.Context(), &genai.ClientConfig{
		APIKey: "private-key", Backend: genai.BackendGeminiAPI,
		HTTPClient: &http.Client{Transport: failureTestTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"unsupported <unit> {field}: private-key private prompt private@example.test","details":[{"raw":"private response detail"}]}}`))}, nil
		})},
	})
	require.NoError(t, err)
	provider := &geminiAITextProvider{apiKey: "private-key", client: client, modelName: "test"}
	_, err = provider.GenerateText(t.Context(), llm.GenerationRequest{RequestID: "gemini-job", UserPrompt: "private prompt"})
	require.Error(t, err)
	var record map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &record))
	require.EqualValues(t, http.StatusBadRequest, record["http_status"])
	require.Contains(t, record["failure_cause"], "unsupported <unit> {field}")
	require.Equal(t, "gemini-job", record["request_id"])
	for _, private := range []string{"private-key", "private prompt", "private@example.test", "private response detail"} {
		require.NotContains(t, output.String(), private)
	}
}
