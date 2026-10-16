// Package providers implements LLM transports using the provider-neutral llm contracts.
package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/llm"
	"github.com/echovisionlab/geul-api/internal/structured"
	"google.golang.org/genai"
)

const (
	defaultGeminiModelName = "gemini-2.5-flash"
	defaultRequestTimeout  = 30 * time.Second
	// Allow JSON escaping and envelope overhead for the 20MiB document input
	// boundary. Provider output has no independent maximum, so bound its wire
	// response explicitly; error responses only need a small diagnostic prefix.
	maxOpenAICompatibleResponseBodyBytes int64 = 128 << 20
	maxOpenAICompatibleErrorBodyBytes    int64 = 64 << 10
)

// GeminiConfig configures a Gemini text-generation provider.
type GeminiConfig struct {
	APIKey      string
	Model       string
	Temperature *float32
}

// OpenAICompatibleConfig configures an OpenAI chat-completions compatible provider.
type OpenAICompatibleConfig struct {
	APIKey           string
	BaseURL          string
	Model            string
	SupportsJSONMode bool
	Temperature      *float64
	MaxOutputTokens  *int32
}

type geminiAITextProvider struct {
	apiKey      string
	client      *genai.Client
	modelName   string
	temperature *float32
}

type geminiAITextSession struct {
	apiKey      string
	chat        *genai.Chat
	modelName   string
	spec        llm.SessionSpec
	config      *genai.GenerateContentConfig
	privateText []string
}

// NewGeminiProvider constructs a Gemini-backed Provider.
func NewGeminiProvider(config GeminiConfig) (llm.Provider, error) {
	apiKey := strings.TrimSpace(config.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("API key must be configured for Gemini provider")
	}
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini client: %w", err)
	}

	modelName := strings.TrimSpace(config.Model)
	if modelName == "" {
		modelName = defaultGeminiModelName
	}

	return &geminiAITextProvider{
		apiKey:      apiKey,
		client:      client,
		modelName:   modelName,
		temperature: config.Temperature,
	}, nil
}

func (p *geminiAITextProvider) GenerateText(
	ctx context.Context,
	req llm.GenerationRequest,
) (string, error) {
	session, err := p.StartSession(ctx, llm.SessionSpec{
		RequestID:          req.RequestID,
		Action:             req.Action,
		SystemPrompt:       req.SystemPrompt,
		ResponseJSONSchema: req.ResponseJSONSchema,
		ResponseSchemaName: req.ResponseSchemaName,
		Timeout:            req.Timeout,
		Observer:           req.Observer,
	})
	if err != nil {
		return "", err
	}
	defer func() {
		_ = session.Close(ctx)
	}()

	return session.GenerateText(ctx, llm.SessionTurn{
		OperationID: req.RequestID,
		TurnKind:    "single",
		UserPrompt:  req.UserPrompt,
	})
}

func (p *geminiAITextProvider) StartSession(
	ctx context.Context,
	spec llm.SessionSpec,
) (llm.Session, error) {
	config := buildGenerationConfig(llm.GenerationRequest{
		RequestID:          spec.RequestID,
		Action:             spec.Action,
		SystemPrompt:       spec.SystemPrompt,
		ResponseJSONSchema: spec.ResponseJSONSchema,
		ResponseSchemaName: spec.ResponseSchemaName,
	}, p.temperature)

	chat, err := p.client.Chats.Create(ctx, p.modelName, config, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini chat session: %w", err)
	}

	return &geminiAITextSession{
		apiKey:    p.apiKey,
		chat:      chat,
		modelName: p.modelName,
		spec:      spec,
		config:    config,
	}, nil
}

func (p *geminiAITextProvider) ProviderName() string {
	return "gemini"
}

func (p *geminiAITextProvider) ModelName() string {
	return p.modelName
}

func (s *geminiAITextSession) GenerateText(
	ctx context.Context,
	turn llm.SessionTurn,
) (_ string, retErr error) {
	s.privateText = append(s.privateText, turn.UserPrompt)
	defer func() {
		if retErr == nil {
			return
		}
		cause := retErr
		var failure *llm.ProviderFailure
		if errors.As(retErr, &failure) && failure.Unwrap() != nil {
			cause = failure.Unwrap()
		}
		level := slog.LevelError
		if errors.Is(retErr, context.Canceled) {
			level = slog.LevelWarn
		}

		private := append([]string{s.apiKey, s.spec.SystemPrompt}, s.privateText...)
		var apiError genai.APIError
		apiErrorFound := false
		var apiErrorPointer *genai.APIError
		if errors.As(cause, &apiErrorPointer) && apiErrorPointer != nil {
			apiError = *apiErrorPointer
			apiErrorFound = true
		} else {
			apiErrorFound = errors.As(cause, &apiError)
		}
		fields := []any{"provider", "gemini", "model", s.modelName}
		if apiErrorFound {
			// The SDK uses the HTTP status line as Status when Message is
			// an unparsed response body. Treat that body as private.
			apiMessage := apiError.Message
			if apiError.Status == fmt.Sprintf("%d %s", apiError.Code, http.StatusText(apiError.Code)) {
				apiMessage = "[response body omitted]"
			}
			// Error() also renders Details, which can contain raw response data.
			message := fmt.Sprintf("Gemini API HTTP %d: %s: %s", apiError.Code, http.StatusText(apiError.Code), apiMessage)
			cause = errors.New(strings.ReplaceAll(cause.Error(), apiError.Error(), message))
			fields = append(fields, "http_status", apiError.Code, "api_status", failurecause.Message(errors.New(apiError.Status), private...))
		}
		fields = append(fields, failurecause.Attr(cause, private...))
		if failure != nil {
			fields = append(fields, "provider_failure_category", failure.Details().Category, "exception_type", failure.Details().ExceptionType)
		}
		fields = append(fields, "domain", "llm", "event", "llm.provider.generate_failed", "outcome", "failed",
			"request_id", s.spec.RequestID, "action", s.spec.Action, "turn_kind", turn.TurnKind, "operation_id", turn.OperationID)
		slog.Log(ctx, level, "AI provider generation failed", fields...)
	}()
	timeout := requestTimeout(s.spec.Timeout)
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	request := llm.GenerationRequest{
		RequestID:          s.spec.RequestID,
		Action:             s.spec.Action,
		SystemPrompt:       s.spec.SystemPrompt,
		UserPrompt:         turn.UserPrompt,
		ResponseJSONSchema: s.spec.ResponseJSONSchema,
		ResponseSchemaName: s.spec.ResponseSchemaName,
		Timeout:            s.spec.Timeout,
		Observer:           s.spec.Observer,
	}
	startedAt := time.Now()
	if request.Observer != nil {
		request.Observer.RequestStarted(ctx, llm.RequestStartedEvent{
			Request: request, ProviderName: "gemini", ModelName: s.modelName, Timeout: timeout,
			Temperature: s.config.Temperature, ResponseMIMEType: s.config.ResponseMIMEType,
		})
	}

	response, err := s.chat.SendMessage(requestCtx, genai.Part{Text: turn.UserPrompt})
	if err != nil {
		providerErr := geminiProviderFailure(err, requestCtx.Err())
		if request.Observer != nil {
			request.Observer.RequestFailed(ctx, llm.RequestFailedEvent{
				Request: request, ProviderName: "gemini", ModelName: s.modelName, StartedAt: startedAt,
				ContextErr: requestCtx.Err(), Err: providerErr,
			})
		}
		return "", providerErr
	}

	text := strings.TrimSpace(response.Text())
	if request.Observer != nil {
		responseBytes := 0
		if response.SDKHTTPResponse != nil {
			responseBytes = len(response.SDKHTTPResponse.Body)
		}
		request.Observer.ResponseReceived(ctx, llm.ResponseReceivedEvent{
			Request: request, ProviderName: "gemini", ModelName: s.modelName, StartedAt: startedAt,
			ResponseID: response.ResponseID, ModelVersion: response.ModelVersion, ResponseBytes: responseBytes, Text: text,
		})
	}
	if text == "" {
		return "", llm.NewProviderFailure(llm.ProviderFailureDetails{
			Category:      llm.ProviderFailureResponseInvalid,
			ExceptionType: llm.ProviderExceptionEmptyResponse,
		}, nil)
	}

	s.privateText = append(s.privateText, text)
	return text, nil
}

func (s *geminiAITextSession) Close(context.Context) error {
	return nil
}

func requestTimeout(timeout time.Duration) time.Duration {
	if timeout > 0 {
		return timeout
	}
	return defaultRequestTimeout
}

func buildGenerationConfig(req llm.GenerationRequest, temperature *float32) *genai.GenerateContentConfig {
	config := &genai.GenerateContentConfig{
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{{Text: req.SystemPrompt}},
		},
		Temperature: new(float32(0.7)),
	}

	if req.ResponseJSONSchema != nil {
		config.Temperature = new(float32(0.2))
		config.ResponseMIMEType = "application/json"
		config.ResponseJsonSchema = req.ResponseJSONSchema
	}
	if temperature != nil {
		config.Temperature = temperature
	}

	return config
}

type openAICompatibleTextProvider struct {
	apiKey           string
	chatURL          string
	modelName        string
	supportsJSONMode bool
	temperature      *float64
	maxOutputTokens  *int32
	httpClient       *http.Client
}

type openAICompatibleTextSession struct {
	provider *openAICompatibleTextProvider
	spec     llm.SessionSpec
	messages []openAICompatibleMessage
}

type openAICompatibleMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAICompatibleChatRequest struct {
	Model          string                    `json:"model"`
	Messages       []openAICompatibleMessage `json:"messages"`
	Temperature    *float64                  `json:"temperature,omitempty"`
	MaxTokens      *int32                    `json:"max_tokens,omitempty"`
	ResponseFormat structured.Fields         `json:"response_format,omitempty"`
}

type openAICompatibleChatResponse struct {
	Choices []struct {
		Message openAICompatibleMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// NewOpenAICompatibleProvider constructs a Provider for the chat-completions API.
func NewOpenAICompatibleProvider(config OpenAICompatibleConfig) (llm.Provider, error) {
	apiKey := strings.TrimSpace(config.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("API key must be configured for OpenAI-compatible provider")
	}
	modelName := strings.TrimSpace(config.Model)
	if modelName == "" {
		return nil, fmt.Errorf("model must be configured for OpenAI-compatible provider")
	}
	chatURL, err := normalizeOpenAICompatibleChatURL(config.BaseURL)
	if err != nil {
		return nil, err
	}
	return &openAICompatibleTextProvider{
		apiKey:           apiKey,
		chatURL:          chatURL,
		modelName:        modelName,
		supportsJSONMode: config.SupportsJSONMode,
		temperature:      config.Temperature,
		maxOutputTokens:  config.MaxOutputTokens,
		httpClient:       &http.Client{},
	}, nil
}

func normalizeOpenAICompatibleChatURL(baseURL string) (string, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return "", fmt.Errorf("API base URL must be configured for OpenAI-compatible provider")
	}
	if strings.HasSuffix(baseURL, "/chat/completions") {
		return baseURL, nil
	}
	if strings.HasSuffix(baseURL, "/v1") {
		baseURL += "/chat/completions"
	} else if !strings.HasSuffix(baseURL, "/chat/completions") {
		baseURL += "/v1/chat/completions"
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		if err != nil {
			slog.Warn("OpenAI-compatible provider URL rejected", "domain", "llm", "event", "llm.provider.config_rejected", failurecause.Attr(err))
		}
		return "", fmt.Errorf("invalid OpenAI-compatible API base URL %q", baseURL)
	}
	return baseURL, nil
}

func (p *openAICompatibleTextProvider) ProviderName() string {
	return "openai-compatible"
}

func (p *openAICompatibleTextProvider) ModelName() string {
	return p.modelName
}

func (p *openAICompatibleTextProvider) StartSession(
	_ context.Context,
	spec llm.SessionSpec,
) (llm.Session, error) {
	return &openAICompatibleTextSession{
		provider: p,
		spec:     spec,
		messages: []openAICompatibleMessage{
			{Role: "system", Content: spec.SystemPrompt},
		},
	}, nil
}

func (p *openAICompatibleTextProvider) GenerateText(
	ctx context.Context,
	req llm.GenerationRequest,
) (string, error) {
	session, err := p.StartSession(ctx, llm.SessionSpec{
		RequestID:          req.RequestID,
		Action:             req.Action,
		SystemPrompt:       req.SystemPrompt,
		ResponseJSONSchema: req.ResponseJSONSchema,
		ResponseSchemaName: req.ResponseSchemaName,
		Timeout:            req.Timeout,
		Observer:           req.Observer,
	})
	if err != nil {
		return "", err
	}
	defer func() {
		_ = session.Close(ctx)
	}()
	return session.GenerateText(ctx, llm.SessionTurn{
		OperationID: req.RequestID,
		TurnKind:    "single",
		UserPrompt:  req.UserPrompt,
	})
}

func (s *openAICompatibleTextSession) GenerateText(
	ctx context.Context,
	turn llm.SessionTurn,
) (_ string, retErr error) {
	httpStatus := 0
	var providerCause error
	defer func() {
		if retErr == nil {
			return
		}
		cause := retErr
		if providerCause != nil {
			cause = providerCause
		}
		var failure *llm.ProviderFailure
		if errors.As(retErr, &failure) && failure.Unwrap() != nil {
			cause = failure.Unwrap()
		}
		level := slog.LevelError
		if errors.Is(retErr, context.Canceled) {
			level = slog.LevelWarn
		}

		private := []string{s.provider.apiKey, s.spec.SystemPrompt, turn.UserPrompt}
		for _, message := range s.messages {
			private = append(private, message.Content)
		}
		fields := []any{"provider", "openai-compatible", "model", s.provider.modelName, "http_status", httpStatus,
			failurecause.Attr(cause, private...)}
		if failure != nil {
			fields = append(fields, "provider_failure_category", failure.Details().Category, "exception_type", failure.Details().ExceptionType)
		}
		fields = append(fields, "domain", "llm", "event", "llm.provider.generate_failed", "outcome", "failed",
			"request_id", s.spec.RequestID, "action", s.spec.Action, "turn_kind", turn.TurnKind, "operation_id", turn.OperationID)
		slog.Log(ctx, level, "AI provider generation failed", fields...)
	}()
	timeout := requestTimeout(s.spec.Timeout)
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	s.messages = append(s.messages, openAICompatibleMessage{Role: "user", Content: turn.UserPrompt})
	requestPayload := openAICompatibleChatRequest{
		Model:          s.provider.modelName,
		Messages:       s.messages,
		Temperature:    openAICompatibleTemperature(s.spec, s.provider.temperature),
		MaxTokens:      s.provider.maxOutputTokens,
		ResponseFormat: openAICompatibleResponseFormat(s.spec, s.provider.supportsJSONMode),
	}
	body, err := json.Marshal(requestPayload)
	if err != nil {
		return "", llm.NewProviderFailure(llm.ProviderFailureDetails{
			Category:      llm.ProviderFailureInternal,
			ExceptionType: llm.ProviderExceptionRequestEncode,
		}, err)
	}

	httpReq, err := http.NewRequestWithContext(requestCtx, http.MethodPost, s.provider.chatURL, bytes.NewReader(body))
	if err != nil {
		return "", llm.NewProviderFailure(llm.ProviderFailureDetails{
			Category:      llm.ProviderFailureInternal,
			ExceptionType: llm.ProviderExceptionRequestBuild,
		}, err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+s.provider.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	startedAt := time.Now()
	httpResp, err := s.provider.httpClient.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return "", err
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(requestCtx.Err(), context.DeadlineExceeded) {
			return "", llm.NewProviderFailure(llm.ProviderFailureDetails{
				Category:      llm.ProviderFailureUnavailable,
				ExceptionType: llm.ProviderExceptionContextDeadline,
			}, err)
		}
		return "", llm.NewProviderFailure(llm.ProviderFailureDetails{
			Category:      llm.ProviderFailureUnavailable,
			ExceptionType: llm.ProviderExceptionHTTPRequest,
		}, err)
	}
	httpStatus = httpResp.StatusCode
	defer httpResp.Body.Close()

	responseBody, err := readOpenAICompatibleResponseBody(
		httpResp,
		maxOpenAICompatibleResponseBodyBytes,
		maxOpenAICompatibleErrorBodyBytes,
	)
	if err != nil {
		return "", err
	}

	var response openAICompatibleChatResponse
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return "", llm.NewProviderFailure(llm.ProviderFailureDetails{
			Category:      llm.ProviderFailureResponseInvalid,
			ExceptionType: llm.ProviderExceptionResponseDecode,
		}, err)
	}
	if response.Error != nil && response.Error.Message != "" {
		providerCause = errors.New(response.Error.Message)
		return "", llm.NewProviderFailure(llm.ProviderFailureDetails{
			Category:      llm.ProviderFailureRejected,
			ExceptionType: llm.ProviderExceptionProviderResponse,
		}, nil)
	}
	if len(response.Choices) == 0 {
		return "", llm.NewProviderFailure(llm.ProviderFailureDetails{
			Category:      llm.ProviderFailureResponseInvalid,
			ExceptionType: llm.ProviderExceptionEmptyResponse,
		}, nil)
	}
	text := strings.TrimSpace(response.Choices[0].Message.Content)
	if text == "" {
		return "", llm.NewProviderFailure(llm.ProviderFailureDetails{
			Category:      llm.ProviderFailureResponseInvalid,
			ExceptionType: llm.ProviderExceptionEmptyResponse,
		}, nil)
	}

	s.messages = append(s.messages, openAICompatibleMessage{Role: "assistant", Content: text})
	slog.Debug(
		"OpenAI-compatible provider response received",
		"request_id", s.spec.RequestID,
		"action", s.spec.Action,
		"turn_kind", turn.TurnKind,
		"duration_ms", time.Since(startedAt).Milliseconds(),
		"text_len", len(text),
	)
	return text, nil
}

func readOpenAICompatibleResponseBody(
	httpResp *http.Response,
	maxSuccessBodyBytes int64,
	maxErrorBodyBytes int64,
) ([]byte, error) {
	isHTTPError := httpResp.StatusCode < 200 || httpResp.StatusCode >= 300
	maxBodyBytes := maxSuccessBodyBytes
	if isHTTPError {
		maxBodyBytes = maxErrorBodyBytes
	}

	responseBody, err := io.ReadAll(io.LimitReader(httpResp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, llm.NewProviderFailure(llm.ProviderFailureDetails{
			Category:      llm.ProviderFailureUnavailable,
			ExceptionType: llm.ProviderExceptionResponseRead,
		}, err)
	}
	if isHTTPError {
		return nil, llm.NewProviderHTTPFailure(httpResp.StatusCode, llm.ProviderExceptionProviderResponse, nil)
	}
	if int64(len(responseBody)) > maxBodyBytes {
		return nil, llm.NewProviderFailure(llm.ProviderFailureDetails{
			Category:      llm.ProviderFailureResponseInvalid,
			ExceptionType: llm.ProviderExceptionProviderResponse,
		}, nil)
	}
	return responseBody, nil
}

func geminiProviderFailure(err error, requestContextErr error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(requestContextErr, context.DeadlineExceeded) {
		return llm.NewProviderFailure(llm.ProviderFailureDetails{
			Category:      llm.ProviderFailureUnavailable,
			ExceptionType: llm.ProviderExceptionContextDeadline,
		}, err)
	}

	if statusCode, ok := geminiAPIStatusCode(err); ok {
		if statusCode == http.StatusNotFound {
			return llm.NewProviderFailure(llm.ProviderFailureDetails{
				Category:        llm.ProviderFailureConfiguration,
				HTTPStatusClass: "4xx",
				ExceptionType:   llm.ProviderExceptionGenAIAPI,
			}, err)
		}
		return llm.NewProviderHTTPFailure(statusCode, llm.ProviderExceptionGenAIAPI, err)
	}

	return llm.NewProviderFailure(llm.ProviderFailureDetails{
		Category:      llm.ProviderFailureInternal,
		ExceptionType: llm.ProviderExceptionUnknown,
	}, err)
}

func geminiAPIStatusCode(err error) (int, bool) {
	var pointerAPIError *genai.APIError
	if errors.As(err, &pointerAPIError) && pointerAPIError != nil {
		return pointerAPIError.Code, true
	}
	var valueAPIError genai.APIError
	if errors.As(err, &valueAPIError) {
		return valueAPIError.Code, true
	}
	return 0, false
}

func (s *openAICompatibleTextSession) Close(context.Context) error {
	return nil
}

func openAICompatibleTemperature(spec llm.SessionSpec, override *float64) *float64 {
	if override != nil {
		return override
	}
	value := 0.7
	if spec.ResponseJSONSchema != nil {
		value = 0.2
	}
	return &value
}

func openAICompatibleResponseFormat(spec llm.SessionSpec, supportsJSONMode bool) structured.Fields {
	if !supportsJSONMode || spec.ResponseJSONSchema == nil {
		return nil
	}
	return structured.Fields{
		"type": "json_schema",
		"json_schema": structured.Fields{
			"name":   responseSchemaName(spec.ResponseSchemaName),
			"strict": true,
			"schema": spec.ResponseJSONSchema,
		},
	}
}

func responseSchemaName(name string) string {
	if strings.TrimSpace(name) == "" {
		return "response"
	}
	return strings.TrimSpace(name)
}
