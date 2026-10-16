package translation

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/echovisionlab/geul-api/internal/llm"
	"github.com/stretchr/testify/require"
)

type failureLogTransport func(*http.Request) (*http.Response, error)

func (f failureLogTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDeepLFailureLogsCauseBeforeBoundedMapping(t *testing.T) {
	for _, tt := range []struct {
		name      string
		status    int
		body      string
		diagnosis string
		category  llm.ProviderFailureCategory
	}{
		{"decode", 200, `{"document_id":false,"document_key":"private-document-key"}`, "cannot unmarshal bool", llm.ProviderFailureResponseInvalid},
		{"HTTP", 503, `private response for private@example.test`, "HTTP 503: Service Unavailable", llm.ProviderFailureUnavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			generator, err := NewDeepLGenerator("private-token", "https://provider.test")
			require.NoError(t, err)
			concrete := generator.(*deeplTranslationGenerator)
			concrete.client.Transport = failureLogTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tt.status, Body: io.NopCloser(strings.NewReader(tt.body))}, nil
			})
			request := testDeepLDocumentRequest()
			session, err := concrete.StartDocumentSession(t.Context(), request)
			require.NoError(t, err)
			_, err = session.UploadDocument(t.Context(), request)
			require.Error(t, err)
			details, ok := llm.ProviderFailureDetailsFromError(err)
			require.True(t, ok)
			require.Equal(t, tt.category, details.Category)
			var record map[string]any
			require.NoError(t, json.Unmarshal(output.Bytes(), &record))
			require.Equal(t, request.RequestID, record["request_id"])
			require.Equal(t, "deepl", record["provider"])
			require.Contains(t, record["failure_cause"], tt.diagnosis)
			for _, secret := range []string{"private-token", "private-document-key", "private@example.test", "private response"} {
				require.NotContains(t, output.String(), secret)
			}
		})
	}
}

func TestAIResponseFailureLogsPrivateDiagnosticAtProviderBoundary(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	request := testXLIFFRequest(
		testProfile("post", "en", "ko", false, nil),
		XLIFFGroup{ID: "body", TranslationUnit: []XLIFFUnit{{ID: "u1", Source: "Hello"}}},
	)
	_, err := NewAIGenerator(rawResponseAIProvider{text: `{"bundles":false}`}).Translate(t.Context(), request)
	require.ErrorIs(t, err, ErrProviderResponseInvalid)
	require.Equal(t, ErrProviderResponseInvalid.Error()+": invalid JSON envelope", err.Error())
	var record map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &record))
	require.Equal(t, "translation.provider.decode_failed", record["event"])
	require.Equal(t, request.RequestID, record["request_id"])
	require.Equal(t, request.OperationID, record["operation_id"])
	require.Contains(t, record["failure_cause"], "cannot unmarshal bool")
}
