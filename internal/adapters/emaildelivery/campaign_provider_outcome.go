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
	result, err := campaign.ApplySESProviderOutcome(
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
	return emaildelivery.SESProviderOutcomeResult{
		MatchedRecipientEmails: result.MatchedRecipientEmails,
		UpdatedRecipients:      result.UpdatedRecipients,
	}, nil
}
