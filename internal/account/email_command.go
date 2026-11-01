package account

import (
	"context"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/failurecause"
)

// Email is one email command of the Account lifecycle: a template, its data
// and the recipient context the worker re-checks before delivery.
type Email struct {
	Recipient    string
	TemplateType string
	TemplateData map[string]string
	// AccountIdentityID names the Identity whose account-selected primary
	// email the worker re-checks; SystemReason is the reason of a system
	// email sent straight to Recipient. Exactly one is set.
	AccountIdentityID string
	SystemReason      string
	ReferenceID       *string
	Locale            *string
	// MessageID is the stable command ID that deduplicates a retried send.
	MessageID string
}

// EmailPublisher enqueues email commands (email.PublishCommand).
type EmailPublisher interface {
	PublishEmail(ctx context.Context, email Email) error
}

// emailPublishFailure carries a failed email command's identifiers to the
// record that logs the failure, and marks the recipient, template values, and
// any other values its text can echo private.
func emailPublishFailure(err error, job Email, private ...string) error {
	values := append([]string{job.Recipient}, private...)
	for _, value := range job.TemplateData {
		values = append(values, value)
	}
	return failurecause.WithAttrs(
		failurecause.WithPrivate(err, values...),
		slog.String("template_type", job.TemplateType),
		slog.String("command_id", job.MessageID),
	)
}
