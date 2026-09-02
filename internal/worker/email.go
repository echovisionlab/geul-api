package worker

import (
	"context"
	"fmt"
	"strings"

	emaildeliveryadapter "github.com/echovisionlab/geul-api/internal/adapters/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/mq"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type emailSendOutcome string

const (
	emailSendOutcomeAccepted                emailSendOutcome = "accepted"
	emailSendOutcomeTerminalSuppressed      emailSendOutcome = "terminal_failed:suppressed"
	emailSendOutcomeTerminalBlocked         emailSendOutcome = "terminal_failed:recipient_blocked"
	emailSendOutcomeTerminalRender          emailSendOutcome = "terminal_failed:render"
	emailSendOutcomeTerminalMissingTemplate emailSendOutcome = "terminal_failed:template_not_configured"
	emailSendOutcomeTerminalNoAdapter       emailSendOutcome = "terminal_failed:no_active_adapter"
	emailSendOutcomeTerminalPermanent       emailSendOutcome = "terminal_failed:permanent_adapter_failure"
	emailSendOutcomeTerminalExpired         emailSendOutcome = "terminal_failed:expired"
	emailDeliveryRetryable                                   = "retryable"
)

func (h *Handlers) handleSendEmail(ctx context.Context, job *managev1.SendEmailEvent) error {
	_, err := h.handleEmailDelivery(ctx, job)
	return err
}

func (h *Handlers) handleEmailDelivery(
	ctx context.Context,
	job *managev1.SendEmailEvent,
) (emailSendOutcome, error) {
	application := emaildelivery.NewDeliveryApplication(
		emaildeliveryadapter.NewCampaignDeliveryStore(h.db, h.auditWriter, h.mailMetrics),
		emaildeliveryadapter.NewRecipientPolicy(h.db, h.kratosClient),
		h.emailDeliveryRenderer(),
		emaildeliveryadapter.NewSuppressionStore(h.db),
		emaildeliveryadapter.NewProviderLoader(h.adapterLoader),
		h.mailMetrics,
	)
	result, err := application.Deliver(ctx, job)
	if err != nil {
		return "", err
	}
	if result.Err != nil {
		if result.Retryable {
			return "", retryableEmailDeliveryError(result.ErrorType)
		}
		return emailSendOutcomeTerminalPermanent, mq.NewTerminalDeliveryError(
			terminalEmailDeliveryErrorClass(result.ErrorType), result.Err,
		)
	}
	return emailSendOutcome(result.Outcome), nil
}

func terminalEmailDeliveryErrorClass(errorType string) string {
	switch errorType {
	case managev1.EmailErrorType_EMAIL_ERROR_TYPE_INVALID_RECIPIENT.String():
		return "email_invalid_recipient"
	case managev1.EmailErrorType_EMAIL_ERROR_TYPE_AUTH_FAILED.String():
		return "email_adapter_auth_failed"
	case managev1.EmailErrorType_EMAIL_ERROR_TYPE_TEMPLATE_ERROR.String():
		return "email_adapter_template_error"
	default:
		return "email_permanent_provider_failure"
	}
}

func retryableEmailDeliveryError(errorType string) error {
	errorType = strings.TrimSpace(errorType)
	if errorType == "" {
		errorType = managev1.EmailErrorType_EMAIL_ERROR_TYPE_UNKNOWN.String()
	}
	return fmt.Errorf("mail delivery retry requested: %s", errorType)
}
