// Package llm provides provider-neutral text-generation contracts and adapters.
package llm

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

	"github.com/echovisionlab/geul-api/internal/structured"
	"google.golang.org/genai"
)

const (
	defaultGeminiModelName = "gemini-2.5-flash"
	defaultRequestTimeout  = 30 * time.Second
)

// GenerationRequest is one stateless text-generation request.
type GenerationRequest struct {
	RequestID          string
	Action             string
	SystemPrompt       string
	UserPrompt         string
	ResponseJSONSchema structured.Fields
	ResponseSchemaName string
	Timeout            time.Duration
	Observer           Observer
}

// SessionSpec is the shared configuration for a multi-turn text session.
type SessionSpec struct {
	RequestID          string
	Action             string
	SystemPrompt       string
	ResponseJSONSchema structured.Fields
	ResponseSchemaName string
	Timeout            time.Duration
	Observer           Observer
}

// SessionTurn is one turn submitted to an established text session.
type SessionTurn struct {
	OperationID string
	TurnKind    string
	UserPrompt  string
}

// Observer receives provider lifecycle events for an individual request.
type Observer interface {
	RequestStarted(context.Context, RequestStartedEvent)
	RequestFailed(context.Context, RequestFailedEvent)
	ResponseReceived(context.Context, ResponseReceivedEvent)
}

// RequestStartedEvent describes a request after provider-specific configuration.
type RequestStartedEvent struct {
	Request          GenerationRequest
	ProviderName     string
	ModelName        string
	Timeout          time.Duration
	Temperature      any
	ResponseMIMEType string
}

// RequestFailedEvent describes a provider request failure.
type RequestFailedEvent struct {
	Request      GenerationRequest
	ProviderName string
	ModelName    string
	StartedAt    time.Time
	ContextErr   error
	Err          error
}

// ResponseReceivedEvent describes a successful provider response.
type ResponseReceivedEvent struct {
	Request       GenerationRequest
	ProviderName  string
	ModelName     string
	StartedAt     time.Time
	ResponseID    string
	ModelVersion  string
	ResponseBytes int
	Text          string
}

// Session is a provider-backed multi-turn text conversation.
type Session interface {
	GenerateText(ctx context.Context, turn SessionTurn) (string, error)
	Close(ctx context.Context) error
}

// Provider is the domain-neutral text-generation boundary used by AI features.
type Provider interface {
	GenerateText(ctx context.Context, req GenerationRequest) (string, error)
	StartSession(ctx context.Context, spec SessionSpec) (Session, error)
	ProviderName() string
	ModelName() string
}

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
	client      *genai.Client
	modelName   string
	temperature *float32
}

type geminiAITextSession struct {
	chat      *genai.Chat
	modelName string
	spec      SessionSpec
	config    *genai.GenerateContentConfig
}

// NewGeminiProvider constructs a Gemini-backed Provider.
func NewGeminiProvider(config GeminiConfig) (Provider, error) {
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
		client:      client,
		modelName:   modelName,
		temperature: config.Temperature,
	}, nil
}

func (p *geminiAITextProvider) GenerateText(
	ctx context.Context,
	req GenerationRequest,
) (string, error) {
	session, err := p.StartSession(ctx, SessionSpec{
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

	return session.GenerateText(ctx, SessionTurn{
		OperationID: req.RequestID,
		TurnKind:    "single",
		UserPrompt:  req.UserPrompt,
	})
}

func (p *geminiAITextProvider) StartSession(
	ctx context.Context,
	spec SessionSpec,
) (Session, error) {
	config := buildGenerationConfig(GenerationRequest{
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
	turn SessionTurn,
) (string, error) {
	timeout := requestTimeout(s.spec.Timeout)
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	request := GenerationRequest{
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
		request.Observer.RequestStarted(ctx, RequestStartedEvent{
			Request: request, ProviderName: "gemini", ModelName: s.modelName, Timeout: timeout,
			Temperature: s.config.Temperature, ResponseMIMEType: s.config.ResponseMIMEType,
		})
	}

	response, err := s.chat.SendMessage(requestCtx, genai.Part{Text: turn.UserPrompt})
	if err != nil {
		providerErr := geminiProviderFailure(err, requestCtx.Err())
		if request.Observer != nil {
			request.Observer.RequestFailed(ctx, RequestFailedEvent{
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
		request.Observer.ResponseReceived(ctx, ResponseReceivedEvent{
			Request: request, ProviderName: "gemini", ModelName: s.modelName, StartedAt: startedAt,
			ResponseID: response.ResponseID, ModelVersion: response.ModelVersion, ResponseBytes: responseBytes, Text: text,
		})
	}
	if text == "" {
		return "", NewProviderFailure(ProviderFailureDetails{
			Category:      ProviderFailureResponseInvalid,
			ExceptionType: ProviderExceptionEmptyResponse,
		}, nil)
	}

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

func buildGenerationConfig(req GenerationRequest, temperature *float32) *genai.GenerateContentConfig {
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
	spec     SessionSpec
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
func NewOpenAICompatibleProvider(config OpenAICompatibleConfig) (Provider, error) {
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
	spec SessionSpec,
) (Session, error) {
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
	req GenerationRequest,
) (string, error) {
	session, err := p.StartSession(ctx, SessionSpec{
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
	return session.GenerateText(ctx, SessionTurn{
		OperationID: req.RequestID,
		TurnKind:    "single",
		UserPrompt:  req.UserPrompt,
	})
}

func (s *openAICompatibleTextSession) GenerateText(
	ctx context.Context,
	turn SessionTurn,
) (string, error) {
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
		return "", NewProviderFailure(ProviderFailureDetails{
			Category:      ProviderFailureInternal,
			ExceptionType: ProviderExceptionRequestEncode,
		}, err)
	}

	httpReq, err := http.NewRequestWithContext(requestCtx, http.MethodPost, s.provider.chatURL, bytes.NewReader(body))
	if err != nil {
		return "", NewProviderFailure(ProviderFailureDetails{
			Category:      ProviderFailureInternal,
			ExceptionType: ProviderExceptionRequestBuild,
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
			return "", NewProviderFailure(ProviderFailureDetails{
				Category:      ProviderFailureUnavailable,
				ExceptionType: ProviderExceptionContextDeadline,
			}, err)
		}
		return "", NewProviderFailure(ProviderFailureDetails{
			Category:      ProviderFailureUnavailable,
			ExceptionType: ProviderExceptionHTTPRequest,
		}, err)
	}
	defer httpResp.Body.Close()

	responseBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return "", NewProviderFailure(ProviderFailureDetails{
			Category:      ProviderFailureUnavailable,
			ExceptionType: ProviderExceptionResponseRead,
		}, err)
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return "", NewProviderHTTPFailure(httpResp.StatusCode, ProviderExceptionProviderResponse, nil)
	}

	var response openAICompatibleChatResponse
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return "", NewProviderFailure(ProviderFailureDetails{
			Category:      ProviderFailureResponseInvalid,
			ExceptionType: ProviderExceptionResponseDecode,
		}, err)
	}
	if response.Error != nil && response.Error.Message != "" {
		return "", NewProviderFailure(ProviderFailureDetails{
			Category:      ProviderFailureRejected,
			ExceptionType: ProviderExceptionProviderResponse,
		}, nil)
	}
	if len(response.Choices) == 0 {
		return "", NewProviderFailure(ProviderFailureDetails{
			Category:      ProviderFailureResponseInvalid,
			ExceptionType: ProviderExceptionEmptyResponse,
		}, nil)
	}
	text := strings.TrimSpace(response.Choices[0].Message.Content)
	if text == "" {
		return "", NewProviderFailure(ProviderFailureDetails{
			Category:      ProviderFailureResponseInvalid,
			ExceptionType: ProviderExceptionEmptyResponse,
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

func geminiProviderFailure(err error, requestContextErr error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(requestContextErr, context.DeadlineExceeded) {
		return NewProviderFailure(ProviderFailureDetails{
			Category:      ProviderFailureUnavailable,
			ExceptionType: ProviderExceptionContextDeadline,
		}, err)
	}

	if statusCode, ok := geminiAPIStatusCode(err); ok {
		if statusCode == http.StatusNotFound {
			return NewProviderFailure(ProviderFailureDetails{
				Category:        ProviderFailureConfiguration,
				HTTPStatusClass: "4xx",
				ExceptionType:   ProviderExceptionGenAIAPI,
			}, err)
		}
		return NewProviderHTTPFailure(statusCode, ProviderExceptionGenAIAPI, err)
	}

	return NewProviderFailure(ProviderFailureDetails{
		Category:      ProviderFailureInternal,
		ExceptionType: ProviderExceptionUnknown,
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

func openAICompatibleTemperature(spec SessionSpec, override *float64) *float64 {
	if override != nil {
		return override
	}
	value := 0.7
	if spec.ResponseJSONSchema != nil {
		value = 0.2
	}
	return &value
}

func openAICompatibleResponseFormat(spec SessionSpec, supportsJSONMode bool) structured.Fields {
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
