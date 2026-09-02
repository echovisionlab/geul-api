package email

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/echovisionlab/geul-api/internal/structured"
)

// Sender sends emails.
type Sender interface {
	Send(ctx context.Context, email *Email) (*SendResult, error)
}

type SendResult struct {
	MessageID string
}

// DeliveryError is the provider adapter's typed delivery decision. Retryable
// is set only when the provider protocol or SDK exposes a retryable failure;
// worker failover must not infer that decision from error text.
type DeliveryError struct {
	Kind      DeliveryErrorKind
	Retryable bool
	Err       error
}

type DeliveryErrorKind string

const (
	DeliveryErrorConnection       DeliveryErrorKind = "connection_timeout"
	DeliveryErrorAuthentication   DeliveryErrorKind = "auth_failed"
	DeliveryErrorRateLimited      DeliveryErrorKind = "rate_limited"
	DeliveryErrorInvalidRecipient DeliveryErrorKind = "invalid_recipient"
	DeliveryErrorTemplate         DeliveryErrorKind = "template_error"
	DeliveryErrorUnknown          DeliveryErrorKind = "unknown"
)

func (e *DeliveryError) Error() string {
	if e == nil || e.Err == nil {
		return "email delivery failed"
	}
	return e.Err.Error()
}

func (e *DeliveryError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func NewDeliveryError(kind DeliveryErrorKind, retryable bool, err error) error {
	if err == nil {
		err = fmt.Errorf("email delivery failed")
	}
	return &DeliveryError{Kind: kind, Retryable: retryable, Err: err}
}

func DeliveryErrorDecision(err error) (DeliveryErrorKind, bool, bool) {
	var deliveryErr *DeliveryError
	if !errors.As(err, &deliveryErr) || deliveryErr == nil {
		return DeliveryErrorUnknown, false, false
	}
	return deliveryErr.Kind, deliveryErr.Retryable, true
}

// Email represents an email to be sent.
type Email struct {
	CommandID string
	MessageID string
	Template  string
	To        string
	Subject   string
	HTML      string
	Text      string
}

func logAdapterLifecycle(
	ctx context.Context,
	level slog.Level,
	message string,
	event string,
	adapterID string,
	adapterName string,
	email *Email,
	providerMessageID string,
	outcome string,
	reason string,
	errorType string,
) {
	attrs := structured.Values{
		"domain", "mail",
		"event", event,
		"adapter_id", strings.TrimSpace(adapterID),
		"adapter_name", strings.TrimSpace(adapterName),
		"outcome", outcome,
	}
	attrs = appendEmailLifecycleAttrs(attrs, email)
	if providerMessageID = strings.TrimSpace(providerMessageID); providerMessageID != "" {
		attrs = append(attrs, "provider_message_id", providerMessageID)
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		attrs = append(attrs, "reason", reason)
	}
	if errorType = strings.TrimSpace(errorType); errorType != "" {
		attrs = append(attrs, "error_type", errorType)
	}
	slog.Log(ctx, level, message, attrs...)
}

func appendEmailLifecycleAttrs(attrs structured.Values, email *Email) structured.Values {
	if email == nil {
		return attrs
	}
	if commandID := strings.TrimSpace(email.CommandID); commandID != "" {
		attrs = append(attrs, "command_id", commandID)
	}
	if logicalMessageID := strings.TrimSpace(email.MessageID); logicalMessageID != "" {
		attrs = append(attrs, "logical_message_id", logicalMessageID)
	}
	if template := strings.TrimSpace(email.Template); template != "" {
		attrs = append(attrs, "template_type", template)
	}
	return attrs
}
