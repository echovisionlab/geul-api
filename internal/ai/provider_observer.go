package ai

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/llm"
	"github.com/echovisionlab/geul-api/internal/structured"
)

type metadataAIProviderObserver struct{}

func (metadataAIProviderObserver) RequestStarted(_ context.Context, event llm.RequestStartedEvent) {
	request := event.Request
	debugPayload := structured.Fields{
		"model":          event.ModelName,
		"systemPrompt":   request.SystemPrompt,
		"userPrompt":     request.UserPrompt,
		"temperature":    event.Temperature,
		"responseMime":   event.ResponseMIMEType,
		"responseSchema": request.ResponseJSONSchema,
	}
	body, _ := json.Marshal(debugPayload)
	payloadHash := sha256.Sum256(body)

	slog.Info("AI provider request started",
		"request_id", request.RequestID,
		"action", request.Action,
		"timeout_ms", event.Timeout.Milliseconds(),
		"system_prompt_len", len(request.SystemPrompt),
		"user_prompt_len", len(request.UserPrompt),
		"payload_bytes", len(body),
		"payload_sha256", fmt.Sprintf("%x", payloadHash[:]),
	)
}

func (metadataAIProviderObserver) RequestFailed(_ context.Context, event llm.RequestFailedEvent) {
	details, ok := llm.ProviderFailureDetailsFromError(event.Err)
	if !ok {
		details = llm.ProviderFailureDetails{
			Category:      llm.ProviderFailureInternal,
			ExceptionType: llm.ProviderExceptionUnknown,
		}
	}
	attrs := structured.Values{
		"request_id", event.Request.RequestID,
		"action", event.Request.Action,
		"duration_ms", time.Since(event.StartedAt).Milliseconds(),
		"context_error", boundedContextError(event.ContextErr),
		"provider_failure_category", string(details.Category),
		"exception_type", string(details.ExceptionType),
	}
	if details.HTTPStatusClass != "" {
		attrs = append(attrs, "http_status_class", details.HTTPStatusClass)
	}

	slog.Error("AI provider request failed", attrs...)
}

func (metadataAIProviderObserver) ResponseReceived(_ context.Context, event llm.ResponseReceivedEvent) {
	slog.Info("AI provider response received",
		"request_id", event.Request.RequestID,
		"action", event.Request.Action,
		"duration_ms", time.Since(event.StartedAt).Milliseconds(),
		"response_id", event.ResponseID,
		"model_version", event.ModelVersion,
		"response_bytes", event.ResponseBytes,
		"text_len", len(event.Text),
	)
}

func boundedContextError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	default:
		return "unknown"
	}
}
