package emaildeliveryadapter

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	"gorm.io/gorm"
)

type CampaignProviderOutcomeStore struct {
	db *gorm.DB
}

func NewCampaignProviderOutcomeStore(db *gorm.DB) *CampaignProviderOutcomeStore {
	if db == nil {
		panic("campaign provider outcome database is required")
	}
	return &CampaignProviderOutcomeStore{db: db}
}

func (s *CampaignProviderOutcomeStore) ApplySESProviderOutcome(
	ctx context.Context,
	event emaildelivery.SESProviderOutcomeEvent,
) (emaildelivery.SESProviderOutcomeResult, error) {
	application, err := campaign.PrepareSESProviderOutcome(
		ctx,
		s.db,
		event.ProviderMessageID,
		campaign.SESProviderOutcome(event.Outcome),
		event.EventAt,
		event.ErrorType,
	)
	if err != nil {
		return emaildelivery.SESProviderOutcomeResult{}, err
	}
	// Each run commits on its own, as the delivery worker's other writes do.
	for _, runID := range application.RunIDs {
		var outcome campaign.SESProviderRunOutcome
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var err error
			outcome, err = application.ApplyToRun(ctx, tx, runID)
			return err
		}); err != nil {
			return emaildelivery.SESProviderOutcomeResult{}, err
		}
		application.Record(outcome)
	}
	result := application.Result()
	return emaildelivery.SESProviderOutcomeResult{
		MatchedRecipientEmails: result.MatchedRecipientEmails,
		UpdatedRecipients:      result.UpdatedRecipients,
	}, nil
}
