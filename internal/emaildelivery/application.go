package emaildelivery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/email"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const (
	RecipientStatusSent            = "sent"
	RecipientStatusSkipped         = "skipped"
	RecipientStatusPermanentFailed = "permanent_failed"
	RecipientStatusBlocked         = "blocked"
	RecipientStatusSuppressed      = "suppressed"
	deliveryRetryableStatus        = "retryable"
)

type DeliveryOutcome string

const (
	DeliveryAccepted         DeliveryOutcome = "accepted"
	DeliverySuppressed       DeliveryOutcome = "terminal_failed:suppressed"
	DeliveryRecipientBlocked DeliveryOutcome = "terminal_failed:recipient_blocked"
	DeliveryRenderFailed     DeliveryOutcome = "terminal_failed:render"
	DeliveryTemplateMissing  DeliveryOutcome = "terminal_failed:template_not_configured"
	DeliveryNoProvider       DeliveryOutcome = "terminal_failed:no_active_adapter"
	DeliveryProviderFailed   DeliveryOutcome = "terminal_failed:permanent_adapter_failure"
	DeliveryExpired          DeliveryOutcome = "terminal_failed:expired"
)

type RecipientDecision struct {
	Blocked bool
	Reason  string
}

// RecipientAuthorizer revalidates the current Account/Member authority facts
// at send time. Its implementation is an adapter; the application owns the
// resulting block/continue transition.
type RecipientAuthorizer interface {
	Authorize(context.Context, *managev1.SendEmailEvent) (RecipientDecision, error)
}

type DeliveryRenderer interface {
	Render(context.Context, *managev1.SendEmailEvent) (*email.RenderedEmail, error)
}

type DeliveryCampaignStore interface {
	NeedsDelivery(context.Context, string) (bool, error)
	MarkResult(context.Context, string, string, string, string) error
}

type Suppression struct {
	Reason string
}

type DeliverySuppressionStore interface {
	Find(context.Context, string) (*Suppression, error)
	Suppress(context.Context, SuppressionRequest) error
}

type ProviderLoader interface {
	GetActiveAdapters(context.Context) ([]email.Adapter, error)
}

type DeliveryMetrics interface {
	RecordSendAttempt(context.Context, string)
	RecordSendResult(context.Context, string, string)
	RecordRecipientStatus(context.Context, string, string)
}

type DeliveryApplication struct {
	campaign     DeliveryCampaignStore
	recipients   RecipientAuthorizer
	renderer     DeliveryRenderer
	suppressions DeliverySuppressionStore
	providers    ProviderLoader
	metrics      DeliveryMetrics
	now          func() time.Time
	sendTimeout  time.Duration
}

func NewDeliveryApplication(
	campaign DeliveryCampaignStore,
	recipients RecipientAuthorizer,
	renderer DeliveryRenderer,
	suppressions DeliverySuppressionStore,
	providers ProviderLoader,
	metrics DeliveryMetrics,
) *DeliveryApplication {
	if campaign == nil || recipients == nil || renderer == nil || suppressions == nil || providers == nil || metrics == nil {
		panic("email delivery application dependencies are required")
	}
	return &DeliveryApplication{
		campaign: campaign, recipients: recipients, renderer: renderer,
		suppressions: suppressions, providers: providers, metrics: metrics,
		now: time.Now, sendTimeout: 4 * time.Minute,
	}
}

type DeliveryResult struct {
	Outcome   DeliveryOutcome
	Retryable bool
	ErrorType string
	Err       error
}

func (a *DeliveryApplication) Deliver(
	ctx context.Context,
	job *managev1.SendEmailEvent,
) (DeliveryResult, error) {
	if job == nil || strings.TrimSpace(job.GetMessageId()) == "" {
		return DeliveryResult{}, fmt.Errorf("email message id is required")
	}
	if expired, err := EmailCommandExpired(job, a.now().UTC()); err != nil {
		return DeliveryResult{}, err
	} else if expired {
		logDeliveryLifecycle(ctx, slog.LevelInfo, "mail.delivery.expired", job, "skipped", "command_expired", "")
		return DeliveryResult{Outcome: DeliveryExpired}, nil
	}

	a.metrics.RecordSendAttempt(ctx, job.GetTemplateType())
	needsDelivery, err := a.campaign.NeedsDelivery(ctx, job.GetDeliveryRecipientId())
	if err != nil || !needsDelivery {
		if err == nil {
			logDeliveryLifecycle(ctx, slog.LevelInfo, "mail.delivery.duplicate_suppressed", job, "skipped", "campaign_recipient_terminal", "")
		}
		return DeliveryResult{}, err
	}

	suppression, err := a.suppressions.Find(ctx, job.GetRecipient())
	if err != nil {
		return DeliveryResult{}, err
	}
	if suppression != nil {
		if err := a.campaign.MarkResult(ctx, job.GetDeliveryRecipientId(), RecipientStatusSuppressed, "", "email_suppressed"); err != nil {
			return DeliveryResult{}, err
		}
		a.metrics.RecordSendResult(ctx, job.GetTemplateType(), "suppressed")
		a.metrics.RecordRecipientStatus(ctx, job.GetTemplateType(), RecipientStatusSuppressed)
		logDeliveryLifecycle(ctx, slog.LevelInfo, "mail.delivery.suppressed", job, "skipped", suppression.Reason, "")
		return DeliveryResult{Outcome: DeliverySuppressed}, nil
	}

	decision, err := a.recipients.Authorize(ctx, job)
	if err != nil {
		return DeliveryResult{}, err
	}
	if decision.Blocked {
		reason := strings.TrimSpace(decision.Reason)
		if reason == "" {
			reason = "recipient_context_blocked"
		}
		if err := a.campaign.MarkResult(ctx, job.GetDeliveryRecipientId(), RecipientStatusBlocked, "", reason); err != nil {
			return DeliveryResult{}, err
		}
		a.metrics.RecordSendResult(ctx, job.GetTemplateType(), "blocked")
		a.metrics.RecordRecipientStatus(ctx, job.GetTemplateType(), RecipientStatusBlocked)
		logDeliveryLifecycle(ctx, slog.LevelWarn, "mail.delivery.blocked", job, "blocked", reason, "recipient_context_blocked")
		return DeliveryResult{Outcome: DeliveryRecipientBlocked}, nil
	}

	rendered, err := a.renderer.Render(ctx, job)
	if err != nil {
		errorType := managev1.EmailErrorType_EMAIL_ERROR_TYPE_TEMPLATE_ERROR.String()
		if markErr := a.campaign.MarkResult(ctx, job.GetDeliveryRecipientId(), RecipientStatusBlocked, "", errorType); markErr != nil {
			return DeliveryResult{}, fmt.Errorf("persist terminal render failure: %w", markErr)
		}
		a.metrics.RecordSendResult(ctx, job.GetTemplateType(), "failed")
		a.metrics.RecordRecipientStatus(ctx, job.GetTemplateType(), RecipientStatusBlocked)
		logDeliveryLifecycle(ctx, slog.LevelError, "mail.delivery.render_failed", job, "failed", "render_failed", errorType)
		return DeliveryResult{Outcome: DeliveryRenderFailed}, nil
	}
	if rendered == nil {
		if err := a.campaign.MarkResult(ctx, job.GetDeliveryRecipientId(), RecipientStatusSkipped, "", "template_not_configured"); err != nil {
			return DeliveryResult{}, err
		}
		a.metrics.RecordSendResult(ctx, job.GetTemplateType(), "skipped")
		a.metrics.RecordRecipientStatus(ctx, job.GetTemplateType(), RecipientStatusSkipped)
		logDeliveryLifecycle(ctx, slog.LevelInfo, "mail.delivery.skipped", job, "skipped", "template_not_configured", "")
		return DeliveryResult{Outcome: DeliveryTemplateMissing}, nil
	}

	adapters, err := a.providers.GetActiveAdapters(ctx)
	if err != nil {
		return DeliveryResult{}, err
	}
	if len(adapters) == 0 {
		if err := a.campaign.MarkResult(ctx, job.GetDeliveryRecipientId(), RecipientStatusBlocked, "", "no_active_adapter"); err != nil {
			return DeliveryResult{}, err
		}
		a.metrics.RecordSendResult(ctx, job.GetTemplateType(), "blocked")
		a.metrics.RecordRecipientStatus(ctx, job.GetTemplateType(), RecipientStatusBlocked)
		logDeliveryLifecycle(ctx, slog.LevelWarn, "mail.delivery.blocked", job, "blocked", "no_active_adapter", "no_active_adapter")
		return DeliveryResult{Outcome: DeliveryNoProvider}, nil
	}

	message := &email.Email{
		CommandID: job.GetMessageId(), MessageID: DurableMessageID(
			job.GetDeliveryRecipientId(), job.GetMessageId(), job.GetReferenceId(),
		),
		Template: job.GetTemplateType(), To: job.GetRecipient(),
		Subject: rendered.Subject, HTML: rendered.HTML, Text: rendered.Text,
	}
	providerMessageID, failures, lastErr, expired, err := a.sendThroughProviders(ctx, job, adapters, message)
	if err != nil {
		return DeliveryResult{}, err
	}
	if expired {
		return DeliveryResult{Outcome: DeliveryExpired}, nil
	}
	if providerMessageID != "" {
		if err := a.campaign.MarkResult(ctx, job.GetDeliveryRecipientId(), RecipientStatusSent, providerMessageID, ""); err != nil {
			return DeliveryResult{}, err
		}
		a.metrics.RecordSendResult(ctx, job.GetTemplateType(), "accepted")
		a.metrics.RecordRecipientStatus(ctx, job.GetTemplateType(), RecipientStatusSent)
		logDeliveryLifecycle(ctx, slog.LevelInfo, "mail.delivery.accepted", job, "accepted", "", "", "provider_message_id", providerMessageID)
		return DeliveryResult{Outcome: DeliveryAccepted}, nil
	}

	status, errorType, failureErr := ClassifyProviderFailures(failures, lastErr)
	retryable := status == deliveryRetryableStatus
	if !retryable && failureErr != nil {
		if err := a.campaign.MarkResult(ctx, job.GetDeliveryRecipientId(), status, "", errorType); err != nil {
			return DeliveryResult{}, err
		}
		a.metrics.RecordSendResult(ctx, job.GetTemplateType(), "failed")
		a.metrics.RecordRecipientStatus(ctx, job.GetTemplateType(), status)
		logDeliveryLifecycle(ctx, slog.LevelError, "mail.delivery.failed", job, "failed", status, errorType)
		if errorType == managev1.EmailErrorType_EMAIL_ERROR_TYPE_INVALID_RECIPIENT.String() {
			if suppressErr := a.suppressions.Suppress(ctx, SuppressionRequest{
				Email: job.GetRecipient(), Reason: EmailSuppressionReasonInvalidRecipient,
				Source:      EmailSuppressionSourceEmailWorker,
				ReferenceID: job.GetReferenceId(), ErrorType: failureErr.Error(),
			}); suppressErr != nil {
				logDeliveryLifecycle(ctx, slog.LevelError, "mail.delivery.suppression_write_failed", job, "failed", "suppression_write_failed", "database_error")
			}
		}
	}
	if failureErr == nil {
		return DeliveryResult{Outcome: DeliveryProviderFailed}, nil
	}
	return DeliveryResult{
		Outcome: DeliveryProviderFailed, Retryable: retryable,
		ErrorType: errorType, Err: failureErr,
	}, nil
}

type ProviderFailure struct {
	Err       error
	ErrorType string
}

func (a *DeliveryApplication) sendThroughProviders(
	ctx context.Context,
	job *managev1.SendEmailEvent,
	adapters []email.Adapter,
	message *email.Email,
) (string, []ProviderFailure, error, bool, error) {
	var failures []ProviderFailure
	var lastErr error
	for _, adapter := range adapters {
		if expired, err := EmailCommandExpired(job, a.now().UTC()); err != nil {
			return "", failures, lastErr, false, err
		} else if expired {
			return "", failures, lastErr, true, nil
		}
		messageID, expired, err := a.sendThroughProvider(ctx, job, adapter, message)
		if expired {
			return "", failures, lastErr, true, nil
		}
		if err == nil {
			logDeliveryLifecycle(ctx, slog.LevelInfo, "mail.adapter.accepted", job, "accepted", "", "", "adapter_id", adapter.ID(), "adapter_name", adapter.Name(), "provider_message_id", messageID)
			return messageID, failures, lastErr, false, nil
		}
		if ctx.Err() != nil {
			return "", failures, lastErr, false, ctx.Err()
		}
		lastErr = err
		errorType, retryable := ProviderErrorDecision(err)
		logDeliveryLifecycle(ctx, slog.LevelError, "mail.adapter.failed", job, "failed", "adapter_send_failed", errorType, "adapter_id", adapter.ID(), "adapter_name", adapter.Name())
		failures = append(failures, ProviderFailure{Err: err, ErrorType: errorType})
		if !retryable {
			break
		}
	}
	return "", failures, lastErr, false, nil
}

func (a *DeliveryApplication) sendThroughProvider(
	ctx context.Context,
	job *managev1.SendEmailEvent,
	adapter email.Adapter,
	message *email.Email,
) (string, bool, error) {
	now := a.now().UTC()
	deadline := now.Add(a.sendTimeout)
	expiresAt, authCommand, err := EmailCommandExpiresAt(job)
	if err != nil {
		return "", true, nil
	}
	if authCommand {
		if !expiresAt.After(now) {
			return "", true, nil
		}
		if expiresAt.Before(deadline) {
			deadline = expiresAt
		}
	}
	sendCtx, cancel := context.WithDeadline(ctx, deadline)
	result, sendErr := adapter.Send(sendCtx, message)
	sendContextErr := sendCtx.Err()
	cancel()
	if sendErr != nil {
		if authCommand && errors.Is(sendContextErr, context.DeadlineExceeded) && !a.now().UTC().Before(expiresAt) {
			return "", true, nil
		}
		return "", false, sendErr
	}
	if result == nil || strings.TrimSpace(result.MessageID) == "" {
		return "", false, fmt.Errorf("mail adapter returned no provider message id")
	}
	return strings.TrimSpace(result.MessageID), false, nil
}

func DurableMessageID(deliveryRecipientID string, referenceIDs ...string) string {
	localID := strings.TrimSpace(deliveryRecipientID)
	if localID == "" {
		for _, candidate := range referenceIDs {
			if candidate = strings.TrimSpace(candidate); candidate != "" {
				localID = candidate
				break
			}
		}
	}
	if localID == "" {
		return ""
	}
	return fmt.Sprintf("<%s@localhost>", localID)
}

func logDeliveryLifecycle(
	ctx context.Context,
	level slog.Level,
	event string,
	job *managev1.SendEmailEvent,
	outcome string,
	reason string,
	errorType string,
	extra ...any,
) {
	attrs := []any{"domain", "mail", "event", event, "outcome", strings.TrimSpace(outcome)}
	if job != nil {
		attrs = append(attrs,
			"command_id", strings.TrimSpace(job.GetMessageId()),
			"template_type", strings.TrimSpace(job.GetTemplateType()),
			"logical_message_id", DurableMessageID(job.GetDeliveryRecipientId(), job.GetMessageId(), job.GetReferenceId()),
		)
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		attrs = append(attrs, "reason", reason)
	}
	if errorType = strings.TrimSpace(errorType); errorType != "" {
		attrs = append(attrs, "error_type", errorType)
	}
	attrs = append(attrs, extra...)
	slog.Log(ctx, level, "Email delivery lifecycle", attrs...)
}

func ClassifyProviderFailures(failures []ProviderFailure, fallback error) (string, string, error) {
	if fallback == nil {
		fallback = fmt.Errorf("email send failed")
	}
	for _, priority := range []string{
		managev1.EmailErrorType_EMAIL_ERROR_TYPE_INVALID_RECIPIENT.String(),
		managev1.EmailErrorType_EMAIL_ERROR_TYPE_AUTH_FAILED.String(),
		managev1.EmailErrorType_EMAIL_ERROR_TYPE_TEMPLATE_ERROR.String(),
	} {
		for _, failure := range failures {
			if failure.ErrorType == priority && failure.Err != nil {
				return StatusForProviderError(priority), priority, failure.Err
			}
		}
	}
	errorType, retryable := ProviderErrorDecision(fallback)
	status := StatusForProviderError(errorType)
	if !retryable && status == deliveryRetryableStatus {
		status = RecipientStatusBlocked
	}
	return status, errorType, fallback
}

func StatusForProviderError(errorType string) string {
	switch errorType {
	case managev1.EmailErrorType_EMAIL_ERROR_TYPE_CONNECTION_TIMEOUT.String(),
		managev1.EmailErrorType_EMAIL_ERROR_TYPE_RATE_LIMITED.String():
		return deliveryRetryableStatus
	case managev1.EmailErrorType_EMAIL_ERROR_TYPE_INVALID_RECIPIENT.String():
		return RecipientStatusPermanentFailed
	case managev1.EmailErrorType_EMAIL_ERROR_TYPE_AUTH_FAILED.String(),
		managev1.EmailErrorType_EMAIL_ERROR_TYPE_TEMPLATE_ERROR.String():
		return RecipientStatusBlocked
	default:
		return deliveryRetryableStatus
	}
}

func ProviderErrorDecision(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return managev1.EmailErrorType_EMAIL_ERROR_TYPE_CONNECTION_TIMEOUT.String(), true
	}
	if kind, retryable, ok := email.DeliveryErrorDecision(err); ok {
		switch kind {
		case email.DeliveryErrorConnection:
			return managev1.EmailErrorType_EMAIL_ERROR_TYPE_CONNECTION_TIMEOUT.String(), retryable
		case email.DeliveryErrorAuthentication:
			return managev1.EmailErrorType_EMAIL_ERROR_TYPE_AUTH_FAILED.String(), retryable
		case email.DeliveryErrorRateLimited:
			return managev1.EmailErrorType_EMAIL_ERROR_TYPE_RATE_LIMITED.String(), retryable
		case email.DeliveryErrorInvalidRecipient:
			return managev1.EmailErrorType_EMAIL_ERROR_TYPE_INVALID_RECIPIENT.String(), retryable
		case email.DeliveryErrorTemplate:
			return managev1.EmailErrorType_EMAIL_ERROR_TYPE_TEMPLATE_ERROR.String(), retryable
		default:
			return managev1.EmailErrorType_EMAIL_ERROR_TYPE_UNKNOWN.String(), retryable
		}
	}
	slog.Debug("Untyped mail adapter failure is terminal")
	return managev1.EmailErrorType_EMAIL_ERROR_TYPE_UNKNOWN.String(), false
}
