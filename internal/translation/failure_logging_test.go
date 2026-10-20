package translation

import (
	"bytes"
	"encoding/json"
	"fmt"
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

func TestAIResponseValidationFailureLogsBoundedIssuesWithJobContext(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	units := make([]XLIFFUnit, 7)
	returned := make([]translationJSONUnit, 7)
	for index := range units {
		id := fmt.Sprintf("u%d", index+1)
		units[index] = XLIFFUnit{ID: id, Source: "Private source sentence"}
		returned[index] = translationJSONUnit{UnitID: id, TranslatedInline: " "}
	}
	request := testXLIFFRequest(testProfile("page", "en", "ko", false, nil), XLIFFGroup{ID: "body", TranslationUnit: units})
	ctx := WithProviderLogContext(t.Context(), ProviderLogContext{
		JobID: "job-1", EntityType: "page", EntityID: "page-9", TargetLocale: "ko", Provider: "gemini", Model: "gemini-test",
	})

	_, err := NewAIGenerator(rawResponseAIProvider{text: translationJSONResponse("body", returned...)}).Translate(ctx, request)
	require.ErrorIs(t, err, ErrProviderResponseInvalid)
	require.Equal(t, ErrProviderResponseInvalid.Error(), err.Error())
	require.Equal(t, []string{"target_empty"}, ProviderResponseIssueKinds(err))
	var record map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &record))
	require.Equal(t, "translation.provider.validation_failed", record["event"])
	require.Equal(t,
		"response violates XLIFF source contract: issues=target_empty:7 sample_ids=target_empty[u1],target_empty[u2],target_empty[u3],target_empty[u4],target_empty[u5],+2",
		record["failure_cause"])
	require.Equal(t, "target_empty", record["response_issues"])
	for key, want := range map[string]string{
		"job_id": "job-1", "entity_type": "page", "entity_id": "page-9", "target_locale": "ko",
		"provider": "gemini", "model": "gemini-test", "operation_id": request.OperationID,
	} {
		require.Equal(t, want, record[key], key)
	}
	require.NotContains(t, output.String(), "Private source")
}

func TestAIResponseInlineFailureLogsSourceGroupAndUnit(t *testing.T) {
	for _, tt := range []struct {
		name, cause, issue string
		units              []translationJSONUnit
	}{
		{
			name:  "missing unit",
			cause: `AI response unit membership changed: group="body" unit="u2" missing`, issue: "missing_unit",
			units: []translationJSONUnit{{UnitID: "u1", TranslatedInline: "번역문"}, {UnitID: "provider-invented-id", TranslatedInline: "번역문"}},
		},
		{
			name:  "inline code",
			cause: `AI response unit inline invalid: group="body" unit="u1": AI inline code identity changed`, issue: "inline_invalid",
			units: []translationJSONUnit{{UnitID: "u1", TranslatedInline: `번역문<ph id="provider-invented-id"/>`}, {UnitID: "u2", TranslatedInline: "번역문"}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			request := testXLIFFRequest(
				testProfile("page", "en", "ko", false, nil),
				XLIFFGroup{ID: "body", TranslationUnit: []XLIFFUnit{{ID: "u1", Source: "Private source"}, {ID: "u2", Source: "Private source"}}},
			)

			_, err := NewAIGenerator(rawResponseAIProvider{text: translationJSONResponse("body", tt.units...)}).Translate(t.Context(), request)
			require.ErrorIs(t, err, ErrProviderResponseInvalid)
			var record map[string]any
			require.NoError(t, json.Unmarshal(output.Bytes(), &record))
			require.Equal(t, "translation.provider.inline_failed", record["event"])
			require.Equal(t, tt.cause, record["failure_cause"])
			require.Equal(t, tt.issue, record["response_issues"])
			for _, private := range []string{"Private source", "번역문", "provider-invented-id"} {
				require.NotContains(t, output.String(), private)
			}
		})
	}
}
