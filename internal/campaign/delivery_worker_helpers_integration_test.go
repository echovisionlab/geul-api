//go:build integration

package campaign

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
)

// The delivery worker ports run in their caller's transaction. These helpers
// open one per call, as internal/adapters/emaildelivery and
// internal/adapters/campaign do.

func claimRecipient(ctx context.Context, db *gorm.DB, recipientID, claimID string) (bool, error) {
	claimed := false
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		claimed, err = ClaimCampaignDeliveryRecipient(ctx, tx, recipientID, claimID)
		return err
	})
	return claimed, err
}

func releaseRecipientClaim(ctx context.Context, db *gorm.DB, recipientID, claimID string) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return ReleaseCampaignDeliveryRecipientClaim(ctx, tx, recipientID, claimID)
	})
}

func markRecipientResult(ctx context.Context, db *gorm.DB, audit domainaudit.Appender, recipientID, status, providerMessageID, errorType string, metrics CampaignDeliveryMetrics) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return MarkCampaignDeliveryRecipientResultWithAudit(ctx, tx, audit, recipientID, status, providerMessageID, errorType, metrics)
	})
}

func markClaimedRecipientResult(ctx context.Context, db *gorm.DB, audit domainaudit.Appender, recipientID, claimID, status, providerMessageID, errorType string, metrics CampaignDeliveryMetrics) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return MarkClaimedCampaignDeliveryRecipientResultWithAudit(ctx, tx, audit, recipientID, claimID, status, providerMessageID, errorType, metrics)
	})
}

func materializeRun(ctx context.Context, db *gorm.DB, spiceDB *auth.SpiceDBClient, runID string) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return MaterializeCampaignDeliveryRun(ctx, tx, spiceDB, runID)
	})
}

// dispatchDueRuns dispatches every due run in its own transaction, skipping
// locked runs and failed ones, and counts the runs handed to delivery.
func dispatchDueRuns(ctx context.Context, db *gorm.DB, dispatcher *CampaignDeliveryDispatcher, limit int) (int, error) {
	runs, err := dispatcher.DueEmailDeliveryRuns(ctx, db, limit)
	if err != nil {
		return 0, err
	}
	dispatched := 0
	for _, run := range runs {
		published := false
		if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var err error
			published, err = dispatcher.DispatchEmailDeliveryRun(ctx, tx, run.ID, true)
			return err
		}); err == nil && published {
			dispatched++
		}
	}
	return dispatched, nil
}

func applySESProviderOutcome(ctx context.Context, db *gorm.DB, providerMessageID string, outcome SESProviderOutcome, eventAt time.Time, errorType string) (SESProviderOutcomeResult, error) {
	application, err := PrepareSESProviderOutcome(ctx, db, providerMessageID, outcome, eventAt, errorType)
	if err != nil {
		return SESProviderOutcomeResult{}, err
	}
	for _, runID := range application.RunIDs {
		var run SESProviderRunOutcome
		if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var err error
			run, err = application.ApplyToRun(ctx, tx, runID)
			return err
		}); err != nil {
			return SESProviderOutcomeResult{}, err
		}
		application.Record(run)
	}
	return application.Result(), nil
}
