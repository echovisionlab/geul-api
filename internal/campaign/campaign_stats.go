package campaign

import (
	"context"
	"strings"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// GetCampaignStatsQuery reads a Campaign's delivery statistics.
type GetCampaignStatsQuery struct {
	ID string
}

// GetCampaignStats returns the latest delivery run's counts, or the stored
// sent count before any run.
func (s *Service) GetCampaignStats(ctx context.Context, query GetCampaignStatsQuery) (*managev1.CampaignStats, error) {
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
	stats := &managev1.CampaignStats{TotalSent: int32(campaign.SentCount)}
	deliveryStats, err := s.emailDelivery.LatestStats(ctx, s.db, query.ID)
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
	Recipients []*managev1.CampaignRecipient
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
	page, err := s.emailDelivery.ListRecipients(ctx, s.db, query.ID, limit, int(query.Offset))
	if err != nil {
		return CampaignRecipientPage{}, err
	}
	if page.Total == 0 && len(page.Recipients) == 0 {
		return CampaignRecipientPage{}, nil
	}
	recipients := make([]*managev1.CampaignRecipient, len(page.Recipients))
	for i, row := range page.Recipients {
		recipients[i] = campaignRecipientMessage(row)
	}
	if err := s.runtime.AppendCampaignRecipientsAccess(ctx, query.ID); err != nil {
		return CampaignRecipientPage{}, err
	}
	return CampaignRecipientPage{Recipients: recipients, Total: int32(page.Total)}, nil
}

func campaignRecipientMessage(row CampaignDeliveryRecipient) *managev1.CampaignRecipient {
	recipient := &managev1.CampaignRecipient{
		Email:    row.Email,
		Status:   row.Status,
		MemberId: strings.TrimSpace(row.MemberID),
	}
	if row.TerminalAt != nil {
		recipient.TerminalAt = timestamppb.New(*row.TerminalAt)
		if row.Status == CampaignDeliveryRecipientStatusSent ||
			row.Status == CampaignDeliveryRecipientStatusDelivered {
			recipient.SentAt = timestamppb.New(*row.TerminalAt)
		}
	}
	if row.ErrorType != nil {
		recipient.ErrorType = row.ErrorType
	}
	return recipient
}
