package campaign

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

// ApplySESProviderOutcome applies an authenticated later SES outcome to
// campaign business history. It never changes the provider-acceptance decision
// and intentionally creates no callback/transport ledger.
func ApplySESProviderOutcome(
	ctx context.Context,
	db *gorm.DB,
	providerMessageID string,
	outcome SESProviderOutcome,
	eventAt time.Time,
	errorType string,
) (SESProviderOutcomeResult, error) {
	providerMessageID, eventAt, errorType, err := normalizeSESProviderOutcomeInput(
		providerMessageID, outcome, eventAt, errorType,
	)
	if err != nil {
		return SESProviderOutcomeResult{}, err
	}
	runIDs, err := loadSESProviderOutcomeRunIDs(ctx, db, providerMessageID)
	if err != nil {
		return SESProviderOutcomeResult{}, err
	}
	result := SESProviderOutcomeResult{}
	seenEmails := map[string]struct{}{}
	for _, runID := range runIDs {
		runResult, err := applySESProviderOutcomeToRun(
			ctx, db, runID, providerMessageID, outcome, eventAt, errorType,
		)
		if err != nil {
			return SESProviderOutcomeResult{}, err
		}
		mergeSESProviderRunOutcome(&result, seenEmails, runResult)
	}
	return result, nil
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

func loadSESProviderOutcomeRunIDs(ctx context.Context, db *gorm.DB, providerMessageID string) ([]string, error) {
	var runIDs []string
	err := db.WithContext(ctx).
		Model(&model.CampaignDeliveryRecipient{}).
		Distinct("run_id").
		Where("provider_message_id = ?", providerMessageID).
		Order("run_id ASC").
		Pluck("run_id", &runIDs).Error
	return runIDs, err
}

type sesProviderRunOutcome struct {
	emails  []string
	updated int
}

func applySESProviderOutcomeToRun(
	ctx context.Context,
	db *gorm.DB,
	runID string,
	providerMessageID string,
	outcome SESProviderOutcome,
	eventAt time.Time,
	errorType string,
) (sesProviderRunOutcome, error) {
	var result sesProviderRunOutcome
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		locked, _, err := lockEmailDeliveryRunForMutation(ctx, tx, runID, "")
		if err != nil {
			return err
		}
		recipients, err := lockSESProviderOutcomeRecipients(ctx, tx, locked.Run.ID, providerMessageID)
		if err != nil {
			return err
		}
		for _, recipient := range recipients {
			updated, err := applySESProviderRecipientOutcome(tx, recipient, outcome, eventAt, errorType)
			if err != nil {
				return err
			}
			result.updated += updated
			result.emails = appendNormalizedEmail(result.emails, recipient.RecipientEmail)
		}
		return refreshEmailDeliveryRunBusinessCounts(ctx, tx, locked.Run.ID)
	})
	return result, err
}

func lockSESProviderOutcomeRecipients(
	ctx context.Context,
	tx *gorm.DB,
	runID string,
	providerMessageID string,
) ([]model.CampaignDeliveryRecipient, error) {
	var recipients []model.CampaignDeliveryRecipient
	err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("run_id = ? AND provider_message_id = ?", runID, providerMessageID).
		Order("id ASC").
		Find(&recipients).Error
	return recipients, err
}

func applySESProviderRecipientOutcome(
	tx *gorm.DB,
	recipient model.CampaignDeliveryRecipient,
	outcome SESProviderOutcome,
	eventAt time.Time,
	errorType string,
) (int, error) {
	status, apply := nextSESProviderRecipientStatus(recipient.Status, outcome)
	if !apply {
		return 0, nil
	}
	updates := structured.Fields{
		"status": status, "terminal_at": eventAt, "updated_at": eventAt,
		"error_type": sesProviderOutcomeErrorType(status, errorType),
	}
	updated := tx.Model(&model.CampaignDeliveryRecipient{}).
		Where("id = ? AND status = ?", recipient.ID, recipient.Status).
		Updates(updates)
	return int(updated.RowsAffected), updated.Error
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
	runResult sesProviderRunOutcome,
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

func refreshEmailDeliveryRunBusinessCounts(ctx context.Context, tx *gorm.DB, runID string) error {
	var counts campaignDeliveryCompletionCounts
	if err := tx.WithContext(ctx).
		Model(&model.CampaignDeliveryRecipient{}).
		Select(`
			COUNT(*) AS total,
			COUNT(*) FILTER (WHERE status = ?) AS pending,
			COUNT(*) FILTER (WHERE status = ?) AS sent,
			COUNT(*) FILTER (WHERE status = ?) AS delivered,
			COUNT(*) FILTER (WHERE status = ?) AS skipped,
			COUNT(*) FILTER (WHERE status = ?) AS permanent_fail,
			COUNT(*) FILTER (WHERE status = ?) AS blocked,
			COUNT(*) FILTER (WHERE status = ?) AS suppressed,
			COUNT(*) FILTER (WHERE status = ?) AS bounced,
			COUNT(*) FILTER (WHERE status = ?) AS complained
		`,
			CampaignDeliveryRecipientStatusPending,
			CampaignDeliveryRecipientStatusSent,
			CampaignDeliveryRecipientStatusDelivered,
			CampaignDeliveryRecipientStatusSkipped,
			CampaignDeliveryRecipientStatusPermanentFailed,
			CampaignDeliveryRecipientStatusBlocked,
			CampaignDeliveryRecipientStatusSuppressed,
			CampaignDeliveryRecipientStatusBounced,
			CampaignDeliveryRecipientStatusComplained,
		).
		Where("run_id = ?", runID).
		Scan(&counts).Error; err != nil {
		return err
	}
	return tx.WithContext(ctx).
		Model(&model.CampaignDeliveryRun{}).
		Where("id = ?", runID).
		Updates(structured.Fields{
			"sent_count":       int(counts.Sent + counts.Delivered),
			"skipped_count":    int(counts.Skipped),
			"failed_count":     int(counts.PermanentFail + counts.Bounced + counts.Complained),
			"blocked_count":    int(counts.Blocked),
			"suppressed_count": int(counts.Suppressed),
		}).Error
}
