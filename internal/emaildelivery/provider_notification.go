package emaildelivery

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
)

// ProviderNotification is the provider-neutral callback input accepted by the
// EmailDelivery application. Signature verification and provider JSON parsing
// remain HTTP-adapter responsibilities.
type ProviderNotification struct {
	Type               string
	ProviderMessageID  string
	OccurredAt         time.Time
	Recipients         []string
	FallbackRecipients []string
	Permanent          bool
	ErrorType          string
}

type SuppressionRequest struct {
	Email       string
	Reason      string
	Source      string
	ReferenceID string
	ErrorType   string
}

// SuppressionWriter persists the EmailDelivery suppression fact without
// exposing GORM or provider transport details to the application.
type SuppressionWriter interface {
	Suppress(context.Context, SuppressionRequest) error
}

type ProviderNotificationProcessor struct {
	outcomes     ProviderOutcomeStore
	suppressions SuppressionWriter
}

func NewProviderNotificationProcessor(
	outcomes ProviderOutcomeStore,
	suppressions SuppressionWriter,
) *ProviderNotificationProcessor {
	if outcomes == nil {
		panic("SES provider outcome store is required")
	}
	if suppressions == nil {
		panic("email suppression writer is required")
	}
	return &ProviderNotificationProcessor{outcomes: outcomes, suppressions: suppressions}
}

func (p *ProviderNotificationProcessor) ProcessProviderNotification(
	ctx context.Context,
	notification ProviderNotification,
) error {
	providerMessageID := strings.TrimSpace(notification.ProviderMessageID)
	if providerMessageID == "" {
		return fmt.Errorf("provider message id is required")
	}
	typeName := strings.ToLower(strings.TrimSpace(notification.Type))
	var outcome SESProviderOutcome
	suppress := false
	switch typeName {
	case "delivery":
		outcome = SESProviderOutcomeDelivered
	case "bounce":
		if !notification.Permanent {
			slog.Info("Ignored non-permanent SES bounce", "domain", "mail", "event", "mail.provider.callback_ignored", "outcome", "skipped", "provider_message_id", providerMessageID, "reason", "non_permanent_bounce")
			return nil
		}
		outcome = SESProviderOutcomeBounced
		suppress = true
	case "complaint":
		outcome = SESProviderOutcomeComplained
		suppress = true
	default:
		return fmt.Errorf("unsupported provider notification type %q", typeName)
	}
	if notification.OccurredAt.IsZero() {
		return fmt.Errorf("provider notification time is required")
	}

	result, err := ApplySESProviderOutcome(
		ctx, p.outcomes, providerMessageID, outcome,
		notification.OccurredAt, notification.ErrorType,
	)
	if err != nil {
		return err
	}
	if suppress {
		recipients := append([]string(nil), notification.Recipients...)
		recipients = append(recipients, result.MatchedRecipientEmails...)
		normalizedRecipients := normalizedUniqueProviderEmails(recipients)
		if len(normalizedRecipients) == 0 && outcome == SESProviderOutcomeComplained {
			normalizedRecipients = normalizedUniqueProviderEmails(notification.FallbackRecipients)
		}
		reason := EmailSuppressionReasonSESBounce
		if outcome == SESProviderOutcomeComplained {
			reason = EmailSuppressionReasonSESComplaint
		}
		for _, recipient := range normalizedRecipients {
			if err := p.suppressions.Suppress(ctx, SuppressionRequest{
				Email: recipient, Reason: reason,
				Source:      EmailSuppressionSourceSESCallback,
				ReferenceID: providerMessageID,
				ErrorType:   strings.TrimSpace(notification.ErrorType),
			}); err != nil {
				return err
			}
		}
	}
	slog.Info(
		"SES callback applied",
		"domain", "mail",
		"event", "mail.provider.callback_applied",
		"outcome", "succeeded",
		"provider_message_id", providerMessageID,
		"provider_outcome", string(outcome),
		"updated_recipients", result.UpdatedRecipients,
	)
	return nil
}

func normalizedUniqueProviderEmails(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		normalized := normalizeSuppressionAddress(value)
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	sort.Strings(result)
	return result
}
