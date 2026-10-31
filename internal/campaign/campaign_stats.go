package campaign

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// GetCampaignStats returns statistics for a sent campaign
func (s *CampaignService) GetCampaignStats(
	ctx context.Context,
	req *connect.Request[managev1.GetCampaignStatsRequest],
) (*connect.Response[managev1.GetCampaignStatsResponse], error) {
	can, canErr := policyv1.Campaign.View(req.Msg.Id)
	if err := s.requireCampaignCan(ctx, can, canErr); err != nil {
		return nil, err
	}

	if req.Msg.Id == "" {
		return nil, errs.Required("id")
	}

	var campaign model.Campaign
	if err := s.db.WithContext(ctx).First(&campaign, "id = ?", req.Msg.Id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.NotFoundMsg("campaign not found")
		}
		return nil, errs.Internal(err)
	}

	stats := &managev1.CampaignStats{
		TotalSent: int32(campaign.SentCount),
	}

	if s.emailDelivery == nil {
		return nil, errs.DependencyUnavailable("Email Delivery")
	}
	deliveryStats, err := s.emailDelivery.LatestStats(ctx, s.db, req.Msg.Id)
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

	return connect.NewResponse(&managev1.GetCampaignStatsResponse{
		Stats: stats,
	}), nil
}

// GetCampaignRecipients returns the recipients of a campaign
func (s *CampaignService) GetCampaignRecipients(
	ctx context.Context,
	req *connect.Request[managev1.GetCampaignRecipientsRequest],
) (*connect.Response[managev1.GetCampaignRecipientsResponse], error) {
	can, canErr := policyv1.Campaign.Manage(req.Msg.Id)
	if err := s.requireCampaignCan(ctx, can, canErr); err != nil {
		return nil, err
	}

	if req.Msg.Id == "" {
		return nil, errs.Required("id")
	}

	limit := int(req.Msg.Limit)
	if limit <= 0 {
		limit = 50
	}
	if limit > 1000 {
		limit = 1000
	}
	offset := int(req.Msg.Offset)

	if s.emailDelivery == nil {
		return nil, errs.DependencyUnavailable("Email Delivery")
	}
	page, err := s.emailDelivery.ListRecipients(
		ctx,
		s.db,
		req.Msg.Id,
		limit,
		offset,
	)
	if err != nil {
		return nil, err
	}
	if page.Total == 0 && len(page.Recipients) == 0 {
		return connect.NewResponse(&managev1.GetCampaignRecipientsResponse{}), nil
	}

	recipients := make([]*managev1.CampaignRecipient, len(page.Recipients))
	for i, row := range page.Recipients {
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

		recipients[i] = recipient
	}
	if err := s.runtime.AppendCampaignRecipientsAccess(ctx, req.Msg.Id); err != nil {
		return nil, err
	}

	return connect.NewResponse(&managev1.GetCampaignRecipientsResponse{
		Recipients: recipients,
		Total:      int32(page.Total),
	}), nil
}
