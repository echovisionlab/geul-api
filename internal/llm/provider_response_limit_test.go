package llm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const expectedMaxProviderErrorBodyRead = 64 << 10

func TestOpenAICompatibleProviderBoundsErrorBodyRead(t *testing.T) {
	const oversizedResponseBytes = 1 << 20
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, strings.Repeat("sensitive-provider-detail", oversizedResponseBytes/24))
	}))
	defer server.Close()

	providerValue, err := NewOpenAICompatibleProvider(OpenAICompatibleConfig{
		APIKey: "test-key", BaseURL: server.URL, Model: "custom-model",
	})
	require.NoError(t, err)
	provider := providerValue.(*openAICompatibleTextProvider)
	counter := &providerResponseBodyCounter{}
	provider.httpClient = &http.Client{
		Transport: providerResponseCountingTransport{next: server.Client().Transport, counter: counter},
	}

	_, err = provider.GenerateText(context.Background(), GenerationRequest{
		RequestID: "req-1", SystemPrompt: "system", UserPrompt: "source text",
	})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "sensitive-provider-detail")
	details, ok := ProviderFailureDetailsFromError(err)
	require.True(t, ok)
	require.Equal(t, ProviderFailureRateLimited, details.Category)
	require.Equal(t, expectedMaxProviderErrorBodyRead+1, counter.bytesRead)
	require.Equal(t, int64(-1), counter.contentLength)
	require.True(t, counter.closed)
}

func TestOpenAICompatibleResponseBodyLimit(t *testing.T) {
	t.Run("rejects oversized success bodies after max plus one bytes", func(t *testing.T) {
		const maxSuccessBodyBytes = 64
		const maxErrorBodyBytes = 16
		const responseText = "sensitive-success-payload"
		response, counter := countedProviderResponse(t, func(w http.ResponseWriter, _ *http.Request) {
			body := strings.Repeat(responseText, 8)
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			_, _ = io.WriteString(w, body)
		})
		require.Equal(t, int64(len(responseText)*8), response.ContentLength)

		_, err := readOpenAICompatibleResponseBody(response, maxSuccessBodyBytes, maxErrorBodyBytes)
		require.Error(t, err)
		require.NotContains(t, err.Error(), responseText)
		details, ok := ProviderFailureDetailsFromError(err)
		require.True(t, ok)
		require.Equal(t, ProviderFailureResponseInvalid, details.Category)
		require.Equal(t, maxSuccessBodyBytes+1, counter.bytesRead)
	})

	t.Run("preserves typed status errors while bounding their bodies", func(t *testing.T) {
		const maxSuccessBodyBytes = 64
		const maxErrorBodyBytes = 16
		const responseText = "sensitive-provider-error"
		response, counter := countedProviderResponse(t, func(w http.ResponseWriter, _ *http.Request) {
			body := strings.Repeat(responseText, 8)
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, body)
		})
		require.Equal(t, int64(len(responseText)*8), response.ContentLength)

		_, err := readOpenAICompatibleResponseBody(response, maxSuccessBodyBytes, maxErrorBodyBytes)
		require.Error(t, err)
		require.NotContains(t, err.Error(), responseText)
		details, ok := ProviderFailureDetailsFromError(err)
		require.True(t, ok)
		require.Equal(t, ProviderFailureRateLimited, details.Category)
		require.Equal(t, "4xx", details.HTTPStatusClass)
		require.Equal(t, maxErrorBodyBytes+1, counter.bytesRead)
	})

	t.Run("bounds chunked success bodies without a content length", func(t *testing.T) {
		const maxSuccessBodyBytes = 64
		const maxErrorBodyBytes = 16
		response, counter := countedProviderResponse(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			_, _ = io.WriteString(w, strings.Repeat("chunk", 32))
		})
		require.Equal(t, int64(-1), response.ContentLength)

		_, err := readOpenAICompatibleResponseBody(response, maxSuccessBodyBytes, maxErrorBodyBytes)
		require.Error(t, err)
		details, ok := ProviderFailureDetailsFromError(err)
		require.True(t, ok)
		require.Equal(t, ProviderFailureResponseInvalid, details.Category)
		require.Equal(t, maxSuccessBodyBytes+1, counter.bytesRead)
	})

	t.Run("accepts a success body exactly at the limit", func(t *testing.T) {
		const maxSuccessBodyBytes = 64
		const maxErrorBodyBytes = 16
		body := strings.Repeat("x", maxSuccessBodyBytes)
		response, counter := countedProviderResponse(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			_, _ = io.WriteString(w, body)
		})

		got, err := readOpenAICompatibleResponseBody(response, maxSuccessBodyBytes, maxErrorBodyBytes)
		require.NoError(t, err)
		require.Equal(t, body, string(got))
		require.Equal(t, maxSuccessBodyBytes, counter.bytesRead)
	})
}

func countedProviderResponse(
	t *testing.T,
	handler http.HandlerFunc,
) (*http.Response, *providerResponseBodyCounter) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	counter := &providerResponseBodyCounter{}
	client := &http.Client{
		Transport: providerResponseCountingTransport{next: server.Client().Transport, counter: counter},
	}
	response, err := client.Get(server.URL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = response.Body.Close() })
	return response, counter
}

type providerResponseBodyCounter struct {
	bytesRead     int
	contentLength int64
	closed        bool
}

type providerResponseCountingTransport struct {
	next    http.RoundTripper
	counter *providerResponseBodyCounter
}

func (transport providerResponseCountingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.next.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	transport.counter.contentLength = response.ContentLength
	response.Body = providerResponseCountingBody{ReadCloser: response.Body, counter: transport.counter}
	return response, nil
}

type providerResponseCountingBody struct {
	io.ReadCloser
	counter *providerResponseBodyCounter
}

func (body providerResponseCountingBody) Read(buffer []byte) (int, error) {
	n, err := body.ReadCloser.Read(buffer)
	body.counter.bytesRead += n
	return n, err
}

func (body providerResponseCountingBody) Close() error {
	body.counter.closed = true
	return body.ReadCloser.Close()
}
