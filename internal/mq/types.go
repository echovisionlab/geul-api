package mq

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/structured"
)

// Message is the transport-neutral message delivered by a durable PGMQ queue.
type Message struct {
	TransportID   int64
	Body          []byte
	Queue         string
	ContentType   string
	MessageID     string
	CorrelationID string
	Timestamp     time.Time
	RetryCount    int
	Redelivered   bool
	Headers       structured.Fields
}

type Handler func(ctx context.Context, msg Message) error

type terminalDeliveryError struct {
	class string
	cause error
}

func (e *terminalDeliveryError) Error() string {
	if e == nil || e.cause == nil {
		return "terminal queue delivery failure"
	}
	return e.cause.Error()
}

func (e *terminalDeliveryError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func NewTerminalDeliveryError(class string, cause error) error {
	if cause == nil {
		cause = fmt.Errorf("terminal queue delivery failure")
	}
	return &terminalDeliveryError{class: normalizeDeliveryErrorClass(class), cause: cause}
}

func TerminalDeliveryErrorClass(err error) (string, bool) {
	var terminalErr *terminalDeliveryError
	if !errors.As(err, &terminalErr) || terminalErr == nil {
		return "", false
	}
	return normalizeDeliveryErrorClass(terminalErr.class), true
}

func normalizeDeliveryErrorClass(class string) string {
	const fallback = "terminal_handler_failure"
	class = strings.TrimSpace(strings.ToLower(class))
	if class == "" || len(class) > 64 {
		return fallback
	}
	for _, char := range class {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') ||
			char == '_' || char == '-' || char == '.' {
			continue
		}
		return fallback
	}
	return class
}

type Middleware func(Handler) Handler

// QueueConfig defines worker behavior. PGMQ owns persistence, visibility and
// archive storage; those transport details are intentionally absent here.
type QueueConfig struct {
	Name         string
	MessageType  string
	Workers      int
	Timeout      time.Duration
	MaxRetries   int
	RetryDelay   time.Duration
	RetryBackoff float64
}

func ChainMiddlewares(handler Handler, middlewares ...Middleware) Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		handler = middlewares[i](handler)
	}
	return handler
}
