package translation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/llm"
	"github.com/echovisionlab/geul-api/internal/localization"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

const (
	DefaultDeepLAPIBaseURL = "https://api.deepl.com"

	deeplDocumentPath        = "/v2/document"
	deeplDocumentFilename    = "translation.xlf"
	deeplDocumentTimeout     = 90 * time.Second
	deeplMaxDocumentBytes    = 20 << 20
	deeplDefaultPollInterval = time.Second
	deeplMaximumPollInterval = 30 * time.Second
)

type deeplTranslationGenerator struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

type deeplTranslationSession struct {
	generator *deeplTranslationGenerator
}

type deeplDocumentHandleResponse struct {
	DocumentID  string `json:"document_id"`
	DocumentKey string `json:"document_key"`
}

type deeplDocumentStatusResponse struct {
	DocumentID       string `json:"document_id"`
	Status           string `json:"status"`
	SecondsRemaining *int   `json:"seconds_remaining,omitempty"`
}

func NewDeepLGenerator(apiKey string, baseURL string) (Generator, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("deeplAPIKey must be configured for deepl translation provider")
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = DefaultDeepLAPIBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid DeepL API base URL %q", baseURL)
	}

	return &deeplTranslationGenerator{
		apiKey: apiKey, baseURL: baseURL,
		client: &http.Client{Timeout: deeplDocumentTimeout},
	}, nil
}

func (g *deeplTranslationGenerator) ProviderName() string { return "deepl" }

func (g *deeplTranslationGenerator) ModelName() string { return "quality_optimized" }

func (g *deeplTranslationGenerator) StartSession(
	_ context.Context,
	req ProviderRequest,
) (GeneratorSession, error) {
	if err := validateDeepLDocumentRequest(req); err != nil {
		return nil, err
	}
	return &deeplTranslationSession{generator: g}, nil
}

func (g *deeplTranslationGenerator) StartDocumentSession(
	_ context.Context,
	req ProviderRequest,
) (ResumableDocumentSession, error) {
	if err := validateDeepLDocumentRequest(req); err != nil {
		return nil, err
	}
	return &deeplTranslationSession{generator: g}, nil
}

// Translate keeps the common synchronous Generator contract for callers that
// do not yet persist document handles. Resumable callers must use
// StartDocumentSession and persist the handle immediately after upload.
func (g *deeplTranslationGenerator) Translate(
	ctx context.Context,
	req ProviderRequest,
) (*ProviderResponse, error) {
	session, err := g.StartDocumentSession(ctx, req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = session.Close(ctx) }()
	return translateDeepLDocumentSynchronously(ctx, session, req)
}

func (s *deeplTranslationSession) Translate(
	ctx context.Context,
	req ProviderRequest,
) (*ProviderResponse, error) {
	if err := validateDeepLDocumentRequest(req); err != nil {
		return nil, err
	}
	return translateDeepLDocumentSynchronously(ctx, s, req)
}

func translateDeepLDocumentSynchronously(
	ctx context.Context,
	session ResumableDocumentSession,
	req ProviderRequest,
) (*ProviderResponse, error) {
	handle, err := session.UploadDocument(ctx, req)
	if err != nil {
		return nil, err
	}
	for {
		status, err := session.CheckDocument(ctx, handle)
		if err != nil {
			return nil, err
		}
		switch status.State {
		case ProviderDocumentPending:
			timer := time.NewTimer(status.PollAfter)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return nil, ctx.Err()
			case <-timer.C:
			}
		case ProviderDocumentComplete:
			return session.DownloadDocument(ctx, req, handle)
		case ProviderDocumentError:
			return nil, llm.NewProviderFailure(llm.ProviderFailureDetails{
				Category:      llm.ProviderFailureRejected,
				ExceptionType: llm.ProviderExceptionProviderResponse,
			}, nil)
		case ProviderDocumentNotFound:
			return nil, deepLDocumentNotFoundFailure()
		default:
			return nil, deepLResponseInvalid(llm.ProviderExceptionProviderResponse)
		}
	}
}

func (s *deeplTranslationSession) UploadDocument(
	ctx context.Context,
	req ProviderRequest,
) (ProviderDocumentHandle, error) {
	if err := validateDeepLDocumentRequest(req); err != nil {
		return ProviderDocumentHandle{}, err
	}
	xliff, err := MarshalXLIFF(&req.Document)
	if err != nil {
		return ProviderDocumentHandle{}, fmt.Errorf("failed to encode DeepL XLIFF document: %w", err)
	}
	if len(xliff) > deeplMaxDocumentBytes {
		return ProviderDocumentHandle{}, fmt.Errorf("DeepL XLIFF document exceeds the upload size limit")
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", deeplDocumentFilename)
	if err != nil {
		return ProviderDocumentHandle{}, fmt.Errorf("failed to build DeepL document upload: %w", err)
	}
	if _, err := file.Write(xliff); err != nil {
		return ProviderDocumentHandle{}, fmt.Errorf("failed to build DeepL document upload: %w", err)
	}
	for _, field := range deepLDocumentFields(req.Profile) {
		if err := writer.WriteField(field.name, field.value); err != nil {
			return ProviderDocumentHandle{}, fmt.Errorf("failed to build DeepL document upload: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return ProviderDocumentHandle{}, fmt.Errorf("failed to build DeepL document upload: %w", err)
	}

	httpReq, cancel, err := s.newRequest(ctx, http.MethodPost, deeplDocumentPath, &body)
	if err != nil {
		return ProviderDocumentHandle{}, deepLRequestBuildFailure(err)
	}
	defer cancel()
	httpReq.Header.Set("Content-Type", writer.FormDataContentType())
	httpResp, err := s.generator.client.Do(httpReq)
	if err != nil {
		return ProviderDocumentHandle{}, deepLTransportError(err)
	}
	defer func() { _ = httpResp.Body.Close() }()
	if !isHTTPSuccess(httpResp.StatusCode) {
		return ProviderDocumentHandle{}, deepLHTTPError(httpResp.StatusCode)
	}

	var decoded deeplDocumentHandleResponse
	if err := decodeDeepLJSON(httpResp.Body, &decoded); err != nil {
		return ProviderDocumentHandle{}, deepLResponseInvalid(llm.ProviderExceptionResponseDecode)
	}
	handle, err := NewProviderDocumentHandle(decoded.DocumentID, decoded.DocumentKey)
	if err != nil {
		return ProviderDocumentHandle{}, deepLResponseInvalid(llm.ProviderExceptionProviderResponse)
	}
	return handle, nil
}

func (s *deeplTranslationSession) CheckDocument(
	ctx context.Context,
	handle ProviderDocumentHandle,
) (ProviderDocumentCheck, error) {
	if err := validateProviderDocumentHandle(handle); err != nil {
		return ProviderDocumentCheck{}, err
	}
	body := strings.NewReader(url.Values{"document_key": []string{handle.DocumentKey()}}.Encode())
	path := deeplDocumentPath + "/" + url.PathEscape(handle.DocumentID())
	httpReq, cancel, err := s.newRequest(ctx, http.MethodPost, path, body)
	if err != nil {
		return ProviderDocumentCheck{}, deepLRequestBuildFailure(err)
	}
	defer cancel()
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpResp, err := s.generator.client.Do(httpReq)
	if err != nil {
		return ProviderDocumentCheck{}, deepLTransportError(err)
	}
	defer func() { _ = httpResp.Body.Close() }()
	if isDeepLDocumentNotFound(httpResp.StatusCode) {
		return ProviderDocumentCheck{}, deepLDocumentNotFoundFailure()
	}
	if !isHTTPSuccess(httpResp.StatusCode) {
		return ProviderDocumentCheck{}, deepLHTTPError(httpResp.StatusCode)
	}

	var decoded deeplDocumentStatusResponse
	if err := decodeDeepLJSON(httpResp.Body, &decoded); err != nil {
		return ProviderDocumentCheck{}, deepLResponseInvalid(llm.ProviderExceptionResponseDecode)
	}
	if strings.TrimSpace(decoded.DocumentID) != handle.DocumentID() {
		return ProviderDocumentCheck{}, deepLResponseInvalid(llm.ProviderExceptionProviderResponse)
	}
	switch decoded.Status {
	case "queued", "translating":
		return ProviderDocumentCheck{
			State: ProviderDocumentPending, PollAfter: deepLPollInterval(decoded.SecondsRemaining),
		}, nil
	case "done":
		return ProviderDocumentCheck{State: ProviderDocumentComplete}, nil
	case "error":
		return ProviderDocumentCheck{}, llm.NewProviderFailure(llm.ProviderFailureDetails{
			Category:      llm.ProviderFailureRejected,
			ExceptionType: llm.ProviderExceptionProviderResponse,
		}, nil)
	default:
		return ProviderDocumentCheck{}, deepLResponseInvalid(llm.ProviderExceptionProviderResponse)
	}
}

func (s *deeplTranslationSession) DownloadDocument(
	ctx context.Context,
	req ProviderRequest,
	handle ProviderDocumentHandle,
) (*ProviderResponse, error) {
	if err := validateDeepLDocumentRequest(req); err != nil {
		return nil, err
	}
	if err := validateProviderDocumentHandle(handle); err != nil {
		return nil, err
	}
	body := strings.NewReader(url.Values{"document_key": []string{handle.DocumentKey()}}.Encode())
	path := deeplDocumentPath + "/" + url.PathEscape(handle.DocumentID()) + "/result"
	httpReq, cancel, err := s.newRequest(ctx, http.MethodPost, path, body)
	if err != nil {
		return nil, deepLRequestBuildFailure(err)
	}
	defer cancel()
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpResp, err := s.generator.client.Do(httpReq)
	if err != nil {
		return nil, deepLTransportError(err)
	}
	defer func() { _ = httpResp.Body.Close() }()
	if isDeepLDocumentNotFound(httpResp.StatusCode) {
		return nil, deepLDocumentNotFoundFailure()
	}
	if !isHTTPSuccess(httpResp.StatusCode) {
		return nil, deepLHTTPError(httpResp.StatusCode)
	}
	xliff, err := io.ReadAll(io.LimitReader(httpResp.Body, deeplMaxDocumentBytes+1))
	if err != nil {
		return nil, llm.NewProviderFailure(llm.ProviderFailureDetails{
			Category:      llm.ProviderFailureUnavailable,
			ExceptionType: llm.ProviderExceptionResponseRead,
		}, err)
	}
	if len(xliff) > deeplMaxDocumentBytes {
		return nil, deepLResponseInvalid(llm.ProviderExceptionProviderResponse)
	}
	raw, err := NewProviderRawResponse("application/xliff+xml", xliff)
	if err != nil {
		return nil, deepLResponseInvalid(llm.ProviderExceptionProviderResponse)
	}
	response, err := ParseTranslatedXLIFF(req.Document, xliff)
	if err != nil {
		return &ProviderResponse{Raw: raw}, deepLResponseInvalid(llm.ProviderExceptionResponseDecode)
	}
	response.Raw = raw
	return response, nil
}

func (s *deeplTranslationSession) Close(context.Context) error { return nil }

func (s *deeplTranslationSession) newRequest(
	ctx context.Context,
	method string,
	path string,
	body io.Reader,
) (*http.Request, context.CancelFunc, error) {
	requestCtx, cancel := context.WithTimeout(ctx, deeplDocumentTimeout)
	httpReq, err := http.NewRequestWithContext(requestCtx, method, s.generator.baseURL+path, body)
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("failed to build DeepL request")
	}
	httpReq.Header.Set("Authorization", "DeepL-Auth-Key "+s.generator.apiKey)
	httpReq.Header.Set("User-Agent", sharedtelemetry.ServiceBackend.Instrumentation("translation"))
	return httpReq, cancel, nil
}

func validateDeepLDocumentRequest(req ProviderRequest) error {
	// Provider-neutral instructions are intentionally not mapped to DeepL. The
	// documented Document API fields are languages, output format, and formality;
	// protected terms are carried by the XLIFF pc/ph structure itself.
	req.Profile.StyleInstructions = nil
	return ValidateProviderRequest(req)
}

func validateProviderDocumentHandle(handle ProviderDocumentHandle) error {
	if strings.TrimSpace(handle.DocumentID()) == "" || strings.TrimSpace(handle.DocumentKey()) == "" {
		return fmt.Errorf("provider document id and key are required")
	}
	return nil
}

type deepLDocumentField struct {
	name  string
	value string
}

func deepLDocumentFields(profile GenerationProfile) []deepLDocumentField {
	fields := []deepLDocumentField{
		{name: "source_lang", value: deepLLanguageCode(profile.SourceLocale, false)},
		{name: "target_lang", value: deepLLanguageCode(profile.TargetLocale, true)},
		{name: "output_format", value: "xlf"},
	}
	if formality := deepLFormality(profile); formality != "" {
		fields = append(fields, deepLDocumentField{name: "formality", value: formality})
	}
	return fields
}

func deepLFormality(profile GenerationProfile) string {
	switch profile.TargetRegister {
	case RegisterPolite, RegisterFormalDocument:
		return "prefer_more"
	case RegisterNeutralPlain:
		if normalizeLocaleLanguage(profile.TargetLocale) == localization.LocaleJapanese {
			return "prefer_less"
		}
	}
	return ""
}

func deepLLanguageCode(locale string, target bool) string {
	normalized := strings.ReplaceAll(strings.TrimSpace(locale), "_", "-")
	if normalized == "" {
		return ""
	}
	lower := strings.ToLower(normalized)
	if !target {
		switch lower {
		case "zh-cn", "zh-hans", "zh-tw", "zh-hant":
			return "ZH"
		}
	}
	if target {
		switch lower {
		case localization.LocaleEnglish:
			return "EN-US"
		case "pt":
			return "PT-BR"
		case "zh-cn", "zh-hans":
			return "ZH-HANS"
		case "zh-tw", "zh-hant":
			return "ZH-HANT"
		}
	}
	if before, after, ok := strings.Cut(lower, "-"); ok {
		return strings.ToUpper(before) + "-" + strings.ToUpper(after)
	}
	return strings.ToUpper(lower)
}

func normalizeLocaleLanguage(locale string) string {
	normalized := strings.ReplaceAll(strings.TrimSpace(locale), "_", "-")
	if before, _, ok := strings.Cut(strings.ToLower(normalized), "-"); ok {
		return before
	}
	return strings.ToLower(normalized)
}

func deepLPollInterval(secondsRemaining *int) time.Duration {
	if secondsRemaining == nil || *secondsRemaining <= 0 {
		return deeplDefaultPollInterval
	}
	interval := time.Duration(*secondsRemaining) * time.Second
	if interval > deeplMaximumPollInterval {
		return deeplMaximumPollInterval
	}
	if interval < deeplDefaultPollInterval {
		return deeplDefaultPollInterval
	}
	return interval
}

func decodeDeepLJSON(body io.Reader, target any) error {
	decoder := json.NewDecoder(io.LimitReader(body, 1<<20))
	return decoder.Decode(target)
}

func isHTTPSuccess(status int) bool {
	return status >= http.StatusOK && status < http.StatusMultipleChoices
}

func isDeepLDocumentNotFound(status int) bool {
	return status == http.StatusNotFound || status == http.StatusGone
}

func deepLHTTPError(status int) error {
	return llm.NewProviderHTTPFailure(status, llm.ProviderExceptionDeepLHTTP, nil)
}

func deepLTransportError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return llm.NewProviderFailure(llm.ProviderFailureDetails{
			Category:      llm.ProviderFailureUnavailable,
			ExceptionType: llm.ProviderExceptionContextDeadline,
		}, err)
	default:
		return llm.NewProviderFailure(llm.ProviderFailureDetails{
			Category:      llm.ProviderFailureUnavailable,
			ExceptionType: llm.ProviderExceptionHTTPRequest,
		}, err)
	}
}

func deepLRequestBuildFailure(err error) error {
	return llm.NewProviderFailure(llm.ProviderFailureDetails{
		Category:      llm.ProviderFailureInternal,
		ExceptionType: llm.ProviderExceptionRequestBuild,
	}, err)
}

func deepLDocumentNotFoundFailure() error {
	return llm.NewProviderFailure(llm.ProviderFailureDetails{
		Category:        llm.ProviderFailureUnavailable,
		HTTPStatusClass: "4xx",
		ExceptionType:   llm.ProviderExceptionDocumentNotFound,
	}, ErrProviderDocumentNotFound)
}

func deepLResponseInvalid(exceptionType llm.ProviderExceptionType) error {
	return llm.NewProviderFailure(llm.ProviderFailureDetails{
		Category:      llm.ProviderFailureResponseInvalid,
		ExceptionType: exceptionType,
	}, ErrProviderResponseInvalid)
}

var _ Generator = (*deeplTranslationGenerator)(nil)
var _ ResumableDocumentGenerator = (*deeplTranslationGenerator)(nil)
var _ GeneratorSession = (*deeplTranslationSession)(nil)
var _ ResumableDocumentSession = (*deeplTranslationSession)(nil)
