package emaildelivery

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type SESProviderOutcome string

const (
	SESProviderOutcomeDelivered  SESProviderOutcome = "delivered"
	SESProviderOutcomeBounced    SESProviderOutcome = "bounced"
	SESProviderOutcomeComplained SESProviderOutcome = "complained"
)

type SESProviderOutcomeEvent struct {
	ProviderMessageID string
	Outcome           SESProviderOutcome
	EventAt           time.Time
	ErrorType         string
}

type SESProviderOutcomeResult struct {
	MatchedRecipientEmails []string
	UpdatedRecipients      int
}

// ProviderOutcomeStore is implemented by a Campaign adapter because delivery
// recipient/run rows are Campaign business history, not provider transport state.
type ProviderOutcomeStore interface {
	ApplySESProviderOutcome(context.Context, SESProviderOutcomeEvent) (SESProviderOutcomeResult, error)
}

func ApplySESProviderOutcome(
	ctx context.Context,
	store ProviderOutcomeStore,
	providerMessageID string,
	outcome SESProviderOutcome,
	eventAt time.Time,
	errorType string,
) (SESProviderOutcomeResult, error) {
	if store == nil {
		return SESProviderOutcomeResult{}, fmt.Errorf("SES provider outcome store is required")
	}
	providerMessageID = strings.TrimSpace(providerMessageID)
	if providerMessageID == "" {
		return SESProviderOutcomeResult{}, fmt.Errorf("SES provider message id is required")
	}
	if !validSESProviderOutcome(outcome) {
		return SESProviderOutcomeResult{}, fmt.Errorf("unsupported SES provider outcome %q", outcome)
	}
	if eventAt.IsZero() {
		eventAt = time.Now()
	}
	return store.ApplySESProviderOutcome(ctx, SESProviderOutcomeEvent{
		ProviderMessageID: providerMessageID,
		Outcome:           outcome,
		EventAt:           eventAt.UTC(),
		ErrorType:         strings.TrimSpace(errorType),
	})
}

func validSESProviderOutcome(outcome SESProviderOutcome) bool {
	switch outcome {
	case SESProviderOutcomeDelivered, SESProviderOutcomeBounced, SESProviderOutcomeComplained:
		return true
	default:
		return false
	}
}
