// Package llm defines provider-neutral text-generation contracts.
package llm

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/structured"
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
