package translation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testXLIFFRequest(profile GenerationProfile, groups ...XLIFFGroup) ProviderRequest {
	return ProviderRequest{
		RequestID: "job-1", OperationID: "test:1", Profile: profile,
		Document: XLIFFDocument{
			Version: XLIFFVersion, SourceLocale: profile.SourceLocale, TargetLocale: profile.TargetLocale,
			File: XLIFFFile{ID: "test:1", Groups: groups},
		},
	}
}

func testProfile(entityType, sourceLocale, targetLocale string, preserveMarkup bool, protectedTerms []string) GenerationProfile {
	profile := GenerationProfile{
		QualityTier: QualityTierHigh, PreserveMarkup: preserveMarkup,
		SourceLocale: sourceLocale, TargetLocale: targetLocale,
		ContentKind: ContentKindEditorial, TargetRegister: RegisterNeutralPlain,
		RegisterPolicy: RegisterPolicyTargetDefault, MIMEType: "text/plain",
		ProtectedTerms: protectedTerms,
	}
	if entityType == "email_template" {
		profile.TargetRegister = RegisterPolite
	}
	switch targetLocale {
	case "ko":
		if profile.TargetRegister == RegisterPolite {
			profile.StyleInstructions = []string{"Use consistent polite Korean appropriate for direct user guidance, using natural -습니다 or -요 endings."}
		} else {
			profile.StyleInstructions = []string{"Use neutral written Korean plain style ending in -다 or -한다; do not mix in polite -습니다 or -요 endings unless the content is direct user guidance."}
		}
	case "ja":
		profile.StyleInstructions = []string{"Use neutral written Japanese plain style, da/de-aru style, and do not mix in desu/masu endings unless the content is direct user guidance."}
	}
	if len(protectedTerms) > 0 {
		profile.StyleInstructions = append(profile.StyleInstructions, "Preserve protected names, titles, handles, URLs, catalog numbers, and canonical entity labels exactly when they appear in the source.")
	}
	return profile
}

func testDeepLDocumentRequest() ProviderRequest {
	profile := testProfile("page", "en", "ko", false, nil)
	return testXLIFFRequest(profile, XLIFFGroup{
		ID: "entity:meta", TranslationUnit: []XLIFFUnit{{ID: "entity:title", Source: "Hello {{name}}"}},
	})
}

func testDeepLDocumentSession(t *testing.T, serverURL string, req ProviderRequest) ResumableDocumentSession {
	t.Helper()
	generator, err := NewDeepLGenerator("test-key", serverURL)
	require.NoError(t, err)
	documentGenerator, ok := generator.(ResumableDocumentGenerator)
	require.True(t, ok)
	session, err := documentGenerator.StartDocumentSession(context.Background(), req)
	require.NoError(t, err)
	return session
}

func testProviderDocumentHandle(t *testing.T, documentID string, documentKey string) ProviderDocumentHandle {
	t.Helper()
	handle, err := NewProviderDocumentHandle(documentID, documentKey)
	require.NoError(t, err)
	return handle
}

func TestDeepLUploadDocumentUsesNativeXLIFFFieldsAndReturnsOpaqueHandle(t *testing.T) {
	t.Parallel()

	req := testDeepLDocumentRequest()
	req.Profile.StyleInstructions = []string{"Use the configured native style."}
	req.Profile.ProtectedTerms = []string{"Hello"}
	require.NoError(t, ProtectXLIFFTerms(&req.Document, req.Profile.ProtectedTerms))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, deeplDocumentPath, r.URL.Path)
		require.Equal(t, "DeepL-Auth-Key test-key", r.Header.Get("Authorization"))
		require.NoError(t, r.ParseMultipartForm(deeplMaxDocumentBytes))
		file, header, err := r.FormFile("file")
		require.NoError(t, err)
		defer func() { _ = file.Close() }()
		require.Equal(t, deeplDocumentFilename, header.Filename)
		xliff, err := io.ReadAll(file)
		require.NoError(t, err)
		document, err := UnmarshalXLIFF(xliff)
		require.NoError(t, err)
		require.Equal(t, req.Document.File.ID, document.File.ID)
		require.Contains(t, string(xliff), `id="protectedTerm1Part1"`)
		require.Contains(t, string(xliff), `canCopy="no" canDelete="no"`)
		assert.Equal(t, "EN", r.FormValue("source_lang"))
		assert.Equal(t, "KO", r.FormValue("target_lang"))
		assert.Equal(t, "xlf", r.FormValue("output_format"))
		assert.Empty(t, r.FormValue("glossary_id"))
		assert.Empty(t, r.FormValue("style_id"))
		assert.Empty(t, r.FormValue("custom_instructions"))
		assert.Empty(t, r.FormValue("translation_memory_id"))
		_, _ = w.Write([]byte(`{"document_id":"doc-secret","document_key":"key-secret"}`))
	}))
	defer server.Close()

	session := testDeepLDocumentSession(t, server.URL, req)
	handle, err := session.UploadDocument(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "doc-secret", handle.DocumentID())
	assert.Equal(t, "key-secret", handle.DocumentKey())
}

func TestDeepLCheckDocumentNormalizesQueuedAndDoneStates(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v2/document/doc-1", r.URL.Path)
		require.NoError(t, r.ParseForm())
		require.Equal(t, "key-1", r.Form.Get("document_key"))
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"document_id":"doc-1","status":"translating","seconds_remaining":120}`))
			return
		}
		_, _ = w.Write([]byte(`{"document_id":"doc-1","status":"done"}`))
	}))
	defer server.Close()

	req := testDeepLDocumentRequest()
	session := testDeepLDocumentSession(t, server.URL, req)
	handle := testProviderDocumentHandle(t, "doc-1", "key-1")
	status, err := session.CheckDocument(context.Background(), handle)
	require.NoError(t, err)
	assert.Equal(t, ProviderDocumentPending, status.State)
	assert.Equal(t, deeplMaximumPollInterval, status.PollAfter)

	status, err = session.CheckDocument(context.Background(), handle)
	require.NoError(t, err)
	assert.Equal(t, ProviderDocumentComplete, status.State)
	assert.Zero(t, status.PollAfter)
}

func TestDeepLDownloadDocumentParsesAndValidatesTranslatedXLIFF(t *testing.T) {
	t.Parallel()

	req := testDeepLDocumentRequest()
	target := "안녕하세요 {{name}}"
	translated := req.Document
	translated.File.Groups[0].TranslationUnit[0].Target = &target
	body, err := MarshalXLIFF(&translated)
	require.NoError(t, err)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v2/document/doc-1/result", r.URL.Path)
		require.NoError(t, r.ParseForm())
		require.Equal(t, "key-1", r.Form.Get("document_key"))
		w.Header().Set("Content-Type", "application/xliff+xml")
		_, _ = w.Write(body)
	}))
	defer server.Close()

	session := testDeepLDocumentSession(t, server.URL, req)
	response, err := session.DownloadDocument(
		context.Background(), req,
		testProviderDocumentHandle(t, "doc-1", "key-1"),
	)
	require.NoError(t, err)
	assert.Equal(t, target, *response.Document.File.Groups[0].TranslationUnit[0].Target)
	require.Len(t, response.Document.File.Groups[0].TranslationUnit[0].TargetInline, 2)
	require.NotNil(t, response.Raw)
	require.Equal(t, "application/xliff+xml", response.Raw.MediaType())
	require.Equal(t, body, response.Raw.Body())
}

func TestDeepLTranslateUsesUploadStatusAndDownloadDocumentFlow(t *testing.T) {
	t.Parallel()

	req := testDeepLDocumentRequest()
	target := "안녕하세요 {{name}}"
	translated := req.Document
	translated.File.Groups[0].TranslationUnit[0].Target = &target
	xliff, err := MarshalXLIFF(&translated)
	require.NoError(t, err)
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case deeplDocumentPath:
			_, _ = w.Write([]byte(`{"document_id":"doc-1","document_key":"key-1"}`))
		case "/v2/document/doc-1":
			_, _ = w.Write([]byte(`{"document_id":"doc-1","status":"done"}`))
		case "/v2/document/doc-1/result":
			_, _ = w.Write(xliff)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	generator, err := NewDeepLGenerator("test-key", server.URL)
	require.NoError(t, err)
	response, err := generator.Translate(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, target, *response.Document.File.Groups[0].TranslationUnit[0].Target)
	assert.Equal(t, []string{deeplDocumentPath, "/v2/document/doc-1", "/v2/document/doc-1/result"}, paths)
}

func TestDeepLDocumentNotFoundAndOtherErrorsAreDistinct(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "missing") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "temporary", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	req := testDeepLDocumentRequest()
	session := testDeepLDocumentSession(t, server.URL, req)
	missing := testProviderDocumentHandle(t, "missing", "wrong-key")
	_, err := session.CheckDocument(context.Background(), missing)
	require.ErrorIs(t, err, ErrProviderDocumentNotFound)
	details, ok := llm.ProviderFailureDetailsFromError(err)
	require.True(t, ok)
	require.Equal(t, llm.ProviderFailureUnavailable, details.Category)
	require.Equal(t, "4xx", details.HTTPStatusClass)
	require.Equal(t, llm.ProviderExceptionDocumentNotFound, details.ExceptionType)

	_, err = session.DownloadDocument(context.Background(), req, missing)
	require.ErrorIs(t, err, ErrProviderDocumentNotFound)
	details, ok = llm.ProviderFailureDetailsFromError(err)
	require.True(t, ok)
	require.Equal(t, llm.ProviderFailureUnavailable, details.Category)
	require.Equal(t, "4xx", details.HTTPStatusClass)
	require.Equal(t, llm.ProviderExceptionDocumentNotFound, details.ExceptionType)

	retained := testProviderDocumentHandle(t, "retained", "still-secret")
	_, err = session.CheckDocument(context.Background(), retained)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrProviderDocumentNotFound)
	assert.NotContains(t, err.Error(), retained.DocumentID())
	assert.NotContains(t, err.Error(), retained.DocumentKey())
}

func TestDeepLHTTPFailuresUseBoundedProviderDetails(t *testing.T) {
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
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const sensitiveResponse = "DeepL response for person@example.test"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(sensitiveResponse))
			}))
			defer server.Close()

			req := testDeepLDocumentRequest()
			session := testDeepLDocumentSession(t, server.URL, req)
			_, err := session.UploadDocument(context.Background(), req)
			require.Error(t, err)
			require.NotContains(t, err.Error(), sensitiveResponse)
			details, ok := llm.ProviderFailureDetailsFromError(err)
			require.True(t, ok)
			require.Equal(t, test.category, details.Category)
			require.Equal(t, test.class, details.HTTPStatusClass)
			require.Equal(t, llm.ProviderExceptionDeepLHTTP, details.ExceptionType)
		})
	}
}

func TestDeepLCheckDocumentReturnsBoundedTerminalProviderError(t *testing.T) {
	t.Parallel()

	handle := testProviderDocumentHandle(t, "doc-sensitive", "key-sensitive")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"document_id":"doc-sensitive","status":"error","error_message":"failed doc-sensitive with key-sensitive"}`))
	}))
	defer server.Close()

	req := testDeepLDocumentRequest()
	session := testDeepLDocumentSession(t, server.URL, req)
	_, err := session.CheckDocument(context.Background(), handle)
	require.Error(t, err)
	require.NotContains(t, err.Error(), handle.DocumentID())
	require.NotContains(t, err.Error(), handle.DocumentKey())
	details, ok := llm.ProviderFailureDetailsFromError(err)
	require.True(t, ok)
	require.Equal(t, llm.ProviderFailureRejected, details.Category)
	require.Empty(t, details.HTTPStatusClass)
	require.Equal(t, llm.ProviderExceptionProviderResponse, details.ExceptionType)
}

func TestDeepLDownloadRejectsMalformedAndIdentityDriftXLIFF(t *testing.T) {
	t.Parallel()

	req := testDeepLDocumentRequest()
	target := "안녕하세요 {{name}}"
	drifted := req.Document
	drifted.File.ID = "page:other"
	drifted.File.Groups[0].TranslationUnit[0].Target = &target
	driftedBody, err := MarshalXLIFF(&drifted)
	require.NoError(t, err)

	tests := []struct {
		name string
		body []byte
	}{
		{name: "malformed", body: []byte(`<xliff`)},
		{name: "identity drift", body: driftedBody},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write(tc.body)
			}))
			defer server.Close()
			session := testDeepLDocumentSession(t, server.URL, req)
			_, err := session.DownloadDocument(context.Background(), req, testProviderDocumentHandle(t, "doc", "key"))
			require.ErrorIs(t, err, ErrProviderResponseInvalid)
			details, ok := llm.ProviderFailureDetailsFromError(err)
			require.True(t, ok)
			require.Equal(t, llm.ProviderFailureResponseInvalid, details.Category)
			require.Equal(t, llm.ProviderExceptionResponseDecode, details.ExceptionType)
		})
	}
}

func TestDeepLDocumentRequestCancellationStopsHTTPCall(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	req := testDeepLDocumentRequest()
	session := testDeepLDocumentSession(t, server.URL, req)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	handle := testProviderDocumentHandle(t, "doc-sensitive", "key-sensitive")
	_, err := session.CheckDocument(ctx, handle)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
	assert.NotContains(t, err.Error(), handle.DocumentID())
	assert.NotContains(t, err.Error(), handle.DocumentKey())
}

func TestDeepLTransportFailuresPreserveCancellationAndDeadline(t *testing.T) {
	cancelled := deepLTransportError(context.Canceled)
	require.ErrorIs(t, cancelled, context.Canceled)
	_, ok := llm.ProviderFailureDetailsFromError(cancelled)
	require.False(t, ok)

	timedOut := deepLTransportError(context.DeadlineExceeded)
	require.ErrorIs(t, timedOut, context.DeadlineExceeded)
	details, ok := llm.ProviderFailureDetailsFromError(timedOut)
	require.True(t, ok)
	require.Equal(t, llm.ProviderFailureUnavailable, details.Category)
	require.Equal(t, llm.ProviderExceptionContextDeadline, details.ExceptionType)
}

func TestDeepLDocumentProfileDropsUnsupportedInstructionsWithoutNativeStyleID(t *testing.T) {
	t.Parallel()

	req := testDeepLDocumentRequest()
	req.Profile.StyleInstructions = []string{"Required style behavior"}
	generator, err := NewDeepLGenerator("test-key", "https://api.deepl.com")
	require.NoError(t, err)
	_, err = generator.(ResumableDocumentGenerator).StartDocumentSession(context.Background(), req)
	require.NoError(t, err)
	require.Empty(t, deepLDocumentFields(req.Profile)[3:], "unsupported instructions must not become provider fields")
}

func TestDeepLDocumentHelpersNormalizeLanguageFormalityAndPollHints(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "EN-US", deepLLanguageCode("en", true))
	assert.Equal(t, "EN", deepLLanguageCode("en", false))
	assert.Equal(t, "ZH-HANS", deepLLanguageCode("zh-CN", true))
	assert.Equal(t, "ZH-HANT", deepLLanguageCode("zh-TW", true))
	assert.Equal(t, "ZH", deepLLanguageCode("zh-Hant", false))
	assert.Equal(t, "PT-BR", deepLLanguageCode("pt", true))
	assert.Equal(t, "PT-PT", deepLLanguageCode("pt-PT", true))

	polite := testProfile("email_template", "en", "ko", false, nil)
	assert.Equal(t, "prefer_more", deepLFormality(polite))
	plainJapanese := testProfile("page", "en", "ja", false, nil)
	assert.Equal(t, "prefer_less", deepLFormality(plainJapanese))
	assert.Equal(t, deeplDefaultPollInterval, deepLPollInterval(nil))
	zero := 0
	assert.Equal(t, deeplDefaultPollInterval, deepLPollInterval(&zero))
	remaining := 10
	assert.Equal(t, 10*time.Second, deepLPollInterval(&remaining))
	remaining = 100
	assert.Equal(t, deeplMaximumPollInterval, deepLPollInterval(&remaining))
}

func TestDeepLDocumentHandleValidationDoesNotMakeHTTPRequest(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()
	req := testDeepLDocumentRequest()
	session := testDeepLDocumentSession(t, server.URL, req)
	_, err := session.CheckDocument(context.Background(), ProviderDocumentHandle{})
	require.ErrorContains(t, err, "id and key are required")
	assert.Zero(t, calls.Load())
}

func TestDeepLStatusRequestUsesFormEncodedDocumentKey(t *testing.T) {
	t.Parallel()

	key := "a+b/c="
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		values, err := url.ParseQuery(string(body))
		require.NoError(t, err)
		require.Equal(t, key, values.Get("document_key"))
		_, _ = w.Write([]byte(`{"document_id":"doc","status":"done"}`))
	}))
	defer server.Close()
	req := testDeepLDocumentRequest()
	session := testDeepLDocumentSession(t, server.URL, req)
	status, err := session.CheckDocument(context.Background(), testProviderDocumentHandle(t, "doc", key))
	require.NoError(t, err)
	assert.Equal(t, ProviderDocumentComplete, status.State)
}

func TestDeepLUploadHTTPErrorDoesNotExposeProviderResponseBody(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "provider-body-secret", http.StatusTooManyRequests)
	}))
	defer server.Close()
	req := testDeepLDocumentRequest()
	session := testDeepLDocumentSession(t, server.URL, req)
	_, err := session.UploadDocument(context.Background(), req)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "provider-body-secret")
}

func TestProviderDocumentNotFoundSentinelSupportsErrorsIs(t *testing.T) {
	t.Parallel()
	require.True(t, errors.Is(ErrProviderDocumentNotFound, ErrProviderDocumentNotFound))
}

func TestProviderDocumentHandleFormattingIsRedacted(t *testing.T) {
	t.Parallel()
	handle := testProviderDocumentHandle(t, "doc-sensitive", "key-sensitive")
	for _, formatted := range []string{
		fmt.Sprint(handle),
		fmt.Sprintf("%+v", handle),
		fmt.Sprintf("%#v", handle),
	} {
		assert.NotContains(t, formatted, handle.DocumentID())
		assert.NotContains(t, formatted, handle.DocumentKey())
	}
}
