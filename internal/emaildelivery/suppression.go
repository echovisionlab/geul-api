package emaildelivery

import (
	"context"
	"errors"
	"strings"
	"time"

	emailutil "github.com/echovisionlab/geul-api/internal/email"
)

const (
	EmailSuppressionReasonInvalidRecipient = "invalid_recipient"
	EmailSuppressionReasonManual           = "manual"
	EmailSuppressionReasonSESBounce        = "bounce"
	EmailSuppressionReasonSESComplaint     = "complaint"

	EmailSuppressionSourceEmailWorker = "email_worker"
	EmailSuppressionSourceAdmin       = "admin"
	EmailSuppressionSourceSESCallback = "ses_callback"
)

// FindActiveSuppression returns the active suppression of email, or nil
// when deliveries to it are not suppressed.
func FindActiveSuppression(ctx context.Context, store Store, email string) (*Suppression, error) {
	normalized := emailutil.NormalizeAddressForDelivery(email)
	if normalized == "" {
		return nil, nil
	}
	suppression, err := store.ActiveSuppression(ctx, normalized)
	if err == nil {
		return &suppression, nil
	}
	if errors.Is(err, ErrSuppressionNotFound) {
		return nil, nil
	}
	return nil, err
}

// LookupActiveSuppressions returns the active suppressions of emails by
// normalized address.
func LookupActiveSuppressions(ctx context.Context, store Store, emails []string) (map[string]Suppression, error) {
	normalizedEmails := normalizeEmailSuppressionList(emails)
	if len(normalizedEmails) == 0 {
		return map[string]Suppression{}, nil
	}

	rows, err := store.ActiveSuppressions(ctx, normalizedEmails)
	if err != nil {
		return nil, err
	}

	result := make(map[string]Suppression, len(rows))
	for _, row := range rows {
		result[emailutil.NormalizeAddressForDelivery(row.Email)] = row
	}
	return result, nil
}

// SuppressAddress records a delivery suppression of request.Email. store is
// bound to the caller's transaction, which must be one: the active
// suppression is locked before it is replaced. An SES callback replay and a
// weaker SES reason leave the active suppression unchanged.
func SuppressAddress(ctx context.Context, store Store, request SuppressionRequest) error {
	normalized := emailutil.NormalizeAddressForDelivery(request.Email)
	if normalized == "" {
		return nil
	}
	reason, source := request.Reason, request.Source
	if reason == "" {
		reason = EmailSuppressionReasonInvalidRecipient
	}
	if source == "" {
		source = EmailSuppressionSourceEmailWorker
	}
	var referenceID *string
	if value := strings.TrimSpace(request.ReferenceID); value != "" {
		referenceID = &value
	}

	now := time.Now()
	var lastError *string
	if strings.TrimSpace(request.ErrorType) != "" {
		value := strings.TrimSpace(request.ErrorType)
		lastError = &value
	}

	if source == EmailSuppressionSourceSESCallback && referenceID != nil {
		replayed, err := store.SuppressionRecorded(ctx, normalized, source, reason, *referenceID)
		if err != nil {
			return err
		}
		if replayed {
			return nil
		}
	}

	existing, err := store.LockActiveSuppression(ctx, normalized)
	if err == nil {
		if source == EmailSuppressionSourceSESCallback &&
			!shouldApplySESSuppression(existing, reason) {
			return nil
		}
		if existing.Reason == reason &&
			existing.Source == source &&
			stringPointerEqual(existing.ReferenceID, referenceID) {
			return nil
		}
		return store.ReplaceSuppression(ctx, existing.ID, Suppression{
			Reason: reason, Source: source, ReferenceID: referenceID, LastError: lastError, SuppressedAt: now,
		})
	}
	if !errors.Is(err, ErrSuppressionNotFound) {
		return err
	}

	return store.InsertSuppression(ctx, Suppression{
		Email:        normalized,
		Reason:       reason,
		Source:       source,
		ReferenceID:  referenceID,
		LastError:    lastError,
		SuppressedAt: now,
	})
}

func shouldApplySESSuppression(existing Suppression, incomingReason string) bool {
	if existing.Source == EmailSuppressionSourceAdmin ||
		existing.Reason == EmailSuppressionReasonManual {
		return false
	}

	return emailSuppressionSeverity(incomingReason) > emailSuppressionSeverity(existing.Reason)
}

func emailSuppressionSeverity(reason string) int {
	switch reason {
	case EmailSuppressionReasonManual:
		return 40
	case EmailSuppressionReasonSESComplaint:
		return 30
	case EmailSuppressionReasonSESBounce:
		return 20
	case EmailSuppressionReasonInvalidRecipient:
		return 10
	default:
		return 0
	}
}

func stringPointerEqual(left *string, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return strings.TrimSpace(*left) == strings.TrimSpace(*right)
}

func normalizeEmailSuppressionList(emails []string) []string {
	seen := make(map[string]struct{}, len(emails))
	result := make([]string, 0, len(emails))
	for _, email := range emails {
		normalized := emailutil.NormalizeAddressForDelivery(email)
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	return result
}

func normalizeSuppressionAddress(value string) string {
	return emailutil.NormalizeAddressForDelivery(value)
}
