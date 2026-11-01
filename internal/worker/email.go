package worker

import (
	"context"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/mq"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const emailDeliveryRetryable = "retryable"

func (h *Handlers) handleEmailDelivery(
	ctx context.Context,
	job *managev1.SendEmailEvent,
) (emaildelivery.DeliveryOutcome, error) {
	result, err := h.emailDelivery.Deliver(ctx, job)
	return emailDeliveryQueueResult(ctx, job, result, err)
}

// emailDeliveryQueueResult maps application outcomes into the queue's
// retryable and terminal error contract.
func emailDeliveryQueueResult(
	ctx context.Context,
	job *managev1.SendEmailEvent,
	result emaildelivery.DeliveryResult,
	deliveryErr error,
) (emaildelivery.DeliveryOutcome, error) {
	if deliveryErr != nil {
		emaildelivery.LogDeliveryFailure(ctx, "mail.worker.delivery_failed", job, deliveryErr)
		return "", deliveryErr
	}
	if result.Err != nil {
		emaildelivery.LogDeliveryFailure(ctx, "mail.worker.provider_failed", job, result.Err, "error_type", result.ErrorType)
		if result.Retryable {
			return "", retryableEmailDeliveryError(result.ErrorType)
		}
		return result.Outcome, mq.NewTerminalDeliveryError(
			terminalEmailDeliveryErrorClass(result.ErrorType), result.Err,
		)
	}
	return result.Outcome, nil
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
