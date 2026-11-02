package campaign

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// GetCampaignStatsQuery reads a Campaign's delivery statistics.
type GetCampaignStatsQuery struct {
	ID string
}

// GetCampaignStats returns the latest delivery run's counts, or the stored
// sent count before any run.
func (s *Service) GetCampaignStats(ctx context.Context, query GetCampaignStatsQuery) (*CampaignStats, error) {
	can, canErr := policyv1.Campaign.View(query.ID)
	if err := s.requireAdminCan(ctx, can, canErr); err != nil {
		return nil, err
	}
	if query.ID == "" {
		return nil, domainerr.Required("id")
	}
	campaign, err := s.loadCampaign(ctx, query.ID)
	if err != nil {
		return nil, err
	}
	stats := &CampaignStats{TotalSent: int32(campaign.SentCount)}
	deliveryStats, err := s.deliveryStats(ctx, query.ID)
	if err != nil {
		return nil, err
	}
	if deliveryStats != nil {
		stats.TotalSent = deliveryStats.TotalSent
		stats.TotalSkipped = deliveryStats.Skipped
		stats.TotalFailed = deliveryStats.Failed
		stats.TotalBlocked = deliveryStats.Blocked
		stats.TotalSuppressed = deliveryStats.Suppressed
	}
	return stats, nil
}

// GetCampaignRecipientsQuery pages through the recipients of a Campaign's
// latest delivery run.
type GetCampaignRecipientsQuery struct {
	ID string
	// Limit defaults to 50 and is capped at 1000.
	Limit  int32
	Offset int32
}

// CampaignRecipientPage is one page of a Campaign's recipients.
type CampaignRecipientPage struct {
	Recipients []CampaignDeliveryRecipient
	Total      int32
}

// GetCampaignRecipients returns one page of recipients and records the
// administrator's access to them.
func (s *Service) GetCampaignRecipients(ctx context.Context, query GetCampaignRecipientsQuery) (CampaignRecipientPage, error) {
	can, canErr := policyv1.Campaign.Manage(query.ID)
	if err := s.requireAdminCan(ctx, can, canErr); err != nil {
		return CampaignRecipientPage{}, err
	}
	if query.ID == "" {
		return CampaignRecipientPage{}, domainerr.Required("id")
	}
	limit := int(query.Limit)
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 1000)
	page, err := s.recipientPage(ctx, query.ID, limit, int(query.Offset))
	if err != nil {
		return CampaignRecipientPage{}, err
	}
	if page.Total == 0 && len(page.Recipients) == 0 {
		return CampaignRecipientPage{}, nil
	}
	recipients := make([]CampaignDeliveryRecipient, len(page.Recipients))
	copy(recipients, page.Recipients)
	if err := s.runtime.AppendCampaignRecipientsAccess(ctx, query.ID); err != nil {
		return CampaignRecipientPage{}, err
	}
	return CampaignRecipientPage{Recipients: recipients, Total: int32(page.Total)}, nil
}

func (s *Service) deliveryStats(ctx context.Context, id string) (*CampaignDeliveryStats, error) {
	var result *CampaignDeliveryStats
	err := s.units.Read(ctx, func(store Store) error { var err error; result, err = store.LatestStats(ctx, id); return err })
	return result, err
}
func (s *Service) recipientPage(ctx context.Context, id string, limit, offset int) (CampaignDeliveryRecipientPage, error) {
	var result CampaignDeliveryRecipientPage
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		result, err = store.ListRecipients(ctx, id, limit, offset)
		return err
	})
	return result, err
}
