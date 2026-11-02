package campaign

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

type SESProviderOutcomeResult struct {
	MatchedRecipientEmails []string
	UpdatedRecipients      int
}

// SESProviderOutcomeApplication applies an authenticated later SES outcome
// to campaign business history. It never changes the provider-acceptance
// decision and intentionally creates no callback/transport ledger. The
// caller applies it to each of RunIDs in its own transaction and records each
// committed run.
type SESProviderOutcomeApplication struct {
	// RunIDs are the delivery runs that sent the message, in lock order.
	RunIDs            []string
	providerMessageID string
	outcome           SESProviderOutcome
	eventAt           time.Time
	errorType         string
	result            SESProviderOutcomeResult
	seenEmails        map[string]struct{}
}

// PrepareSESProviderOutcome validates the outcome and finds the runs that
// sent providerMessageID.
func PrepareSESProviderOutcome(
	ctx context.Context,
	joined Joined,
	providerMessageID string,
	outcome SESProviderOutcome,
	eventAt time.Time,
	errorType string,
) (*SESProviderOutcomeApplication, error) {
	providerMessageID, eventAt, errorType, err := normalizeSESProviderOutcomeInput(
		providerMessageID, outcome, eventAt, errorType,
	)
	if err != nil {
		return nil, err
	}
	runIDs, err := joined.Store.ProviderOutcomeRunIDs(ctx, providerMessageID)
	if err != nil {
		return nil, err
	}
	return &SESProviderOutcomeApplication{
		RunIDs: runIDs, providerMessageID: providerMessageID, outcome: outcome,
		eventAt: eventAt, errorType: errorType, seenEmails: map[string]struct{}{},
	}, nil
}

// ApplyToRun applies the outcome to the run's recipients in tx.
func (a *SESProviderOutcomeApplication) ApplyToRun(ctx context.Context, joined Joined, runID string) (SESProviderRunOutcome, error) {
	return applySESProviderOutcomeToRun(ctx, joined, runID, a.providerMessageID, a.outcome, a.eventAt, a.errorType)
}

// Record adds a committed run's outcome to the result.
func (a *SESProviderOutcomeApplication) Record(run SESProviderRunOutcome) {
	mergeSESProviderRunOutcome(&a.result, a.seenEmails, run)
}

// Result is the outcome of every recorded run.
func (a *SESProviderOutcomeApplication) Result() SESProviderOutcomeResult {
	return a.result
}

func normalizeSESProviderOutcomeInput(
	providerMessageID string,
	outcome SESProviderOutcome,
	eventAt time.Time,
	errorType string,
) (string, time.Time, string, error) {
	providerMessageID = strings.TrimSpace(providerMessageID)
	if providerMessageID == "" {
		return "", time.Time{}, "", fmt.Errorf("SES provider message id is required")
	}
	if !validSESProviderOutcome(outcome) {
		return "", time.Time{}, "", fmt.Errorf("unsupported SES provider outcome %q", outcome)
	}
	if eventAt.IsZero() {
		eventAt = time.Now()
	}
	return providerMessageID, eventAt.UTC(), strings.TrimSpace(errorType), nil
}

// SESProviderRunOutcome is what an outcome changed in one run.
type SESProviderRunOutcome struct {
	emails  []string
	updated int
}

func applySESProviderOutcomeToRun(
	ctx context.Context,
	joined Joined,
	runID string,
	providerMessageID string,
	outcome SESProviderOutcome,
	eventAt time.Time,
	errorType string,
) (SESProviderRunOutcome, error) {
	tx := joined.Store
	var result SESProviderRunOutcome
	locked, _, err := tx.LockDeliveryRun(ctx, runID, false)
	if err != nil {
		return SESProviderRunOutcome{}, err
	}
	recipients, err := tx.LockProviderRecipients(ctx, locked.Run.ID, providerMessageID)
	if err != nil {
		return SESProviderRunOutcome{}, err
	}
	for _, recipient := range recipients {
		updated, err := applySESProviderRecipientOutcome(ctx, tx, recipient, outcome, eventAt, errorType)
		if err != nil {
			return SESProviderRunOutcome{}, err
		}
		result.updated += updated
		result.emails = appendNormalizedEmail(result.emails, recipient.RecipientEmail)
	}
	return result, refreshEmailDeliveryRunBusinessCounts(ctx, joined, locked.Run.ID)
}

func applySESProviderRecipientOutcome(
	ctx context.Context, tx Store,
	recipient DeliveryRecipient,
	outcome SESProviderOutcome,
	eventAt time.Time,
	errorType string,
) (int, error) {
	status, apply := nextSESProviderRecipientStatus(recipient.Status, outcome)
	if !apply {
		return 0, nil
	}

	return tx.UpdateProviderRecipient(ctx, recipient, status, eventAt, sesProviderOutcomeErrorType(status, errorType))
}

func sesProviderOutcomeErrorType(status, errorType string) *string {
	if status != CampaignDeliveryRecipientStatusBounced && status != CampaignDeliveryRecipientStatusComplained {
		return nil
	}
	return nullableTrimmedString(errorType)
}

func appendNormalizedEmail(emails []string, candidate string) []string {
	email := strings.ToLower(strings.TrimSpace(candidate))
	if email == "" {
		return emails
	}
	return append(emails, email)
}

func mergeSESProviderRunOutcome(
	result *SESProviderOutcomeResult,
	seen map[string]struct{},
	runResult SESProviderRunOutcome,
) {
	result.UpdatedRecipients += runResult.updated
	for _, email := range runResult.emails {
		if _, exists := seen[email]; exists {
			continue
		}
		seen[email] = struct{}{}
		result.MatchedRecipientEmails = append(result.MatchedRecipientEmails, email)
	}
}

func validSESProviderOutcome(outcome SESProviderOutcome) bool {
	switch outcome {
	case SESProviderOutcomeDelivered, SESProviderOutcomeBounced, SESProviderOutcomeComplained:
		return true
	default:
		return false
	}
}

func nextSESProviderRecipientStatus(current string, outcome SESProviderOutcome) (string, bool) {
	switch outcome {
	case SESProviderOutcomeDelivered:
		if current == CampaignDeliveryRecipientStatusSent {
			return CampaignDeliveryRecipientStatusDelivered, true
		}
	case SESProviderOutcomeBounced:
		if current == CampaignDeliveryRecipientStatusSent || current == CampaignDeliveryRecipientStatusDelivered {
			return CampaignDeliveryRecipientStatusBounced, true
		}
	case SESProviderOutcomeComplained:
		if current == CampaignDeliveryRecipientStatusSent ||
			current == CampaignDeliveryRecipientStatusDelivered ||
			current == CampaignDeliveryRecipientStatusBounced {
			return CampaignDeliveryRecipientStatusComplained, true
		}
	}
	return current, false
}

func refreshEmailDeliveryRunBusinessCounts(ctx context.Context, joined Joined, runID string) error {

	counts, err := joined.Store.CompletionCounts(ctx, runID)
	if err != nil {
		return err
	}
	return joined.Store.RefreshBusinessCounts(ctx, runID, counts)
}
