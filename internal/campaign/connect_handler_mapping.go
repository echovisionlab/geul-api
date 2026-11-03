package campaign

import (
	"fmt"
	"strings"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func toProtoCampaign(c *CampaignDocument) (*managev1.Campaign, error) {
	recipientScope, err := campaignRecipientScopeToProto(c.RecipientScope)
	if err != nil {
		return nil, err
	}
	campaign := &managev1.Campaign{
		Id:         c.ID,
		Name:       c.Name,
		Subject:    c.Subject,
		Status:     campaignStatusToProto(CampaignStatus(c.Status)),
		TargetMode: campaignTargetModeToProto(c.TargetMode),
		SentCount:  int32(c.SentCount),
		CreatedAt:  timestamppb.New(c.CreatedAt),
		UpdatedAt:  timestamppb.New(c.UpdatedAt),
	}
	if c.ScheduledAt != nil {
		campaign.ScheduledAt = timestamppb.New(*c.ScheduledAt)
	}
	if c.SentAt != nil {
		campaign.SentAt = timestamppb.New(*c.SentAt)
	}
	if c.CriteriaID != nil {
		campaign.MemberCriteriaId = c.CriteriaID
	}
	if c.LayoutID != nil {
		campaign.LayoutId = c.LayoutID
	}
	campaign.RecipientScope = recipientScope
	campaign.Document = c.Document
	campaign.DocumentRevision = c.DocumentRevision
	campaign.SourceLocale = c.SourceLocale
	return campaign, nil
}
func campaignTargetModeToProto(
	mode string,
) managev1.CampaignTargetMode {
	switch strings.TrimSpace(mode) {
	case CampaignTargetModeAll:
		return managev1.CampaignTargetMode_CAMPAIGN_TARGET_MODE_ALL
	case CampaignTargetModeMemberCriteria:
		return managev1.CampaignTargetMode_CAMPAIGN_TARGET_MODE_MEMBER_CRITERIA
	default:
		return managev1.CampaignTargetMode_CAMPAIGN_TARGET_MODE_UNSPECIFIED
	}
}

func campaignRecipientScopeToProto(scope string) (managev1.CampaignRecipientScope, error) {
	switch strings.TrimSpace(scope) {
	case campaignRecipientScopeSubscribedUsers:
		return managev1.CampaignRecipientScope_CAMPAIGN_RECIPIENT_SCOPE_SUBSCRIBED_USERS, nil
	case campaignRecipientScopeAllMatchingUsers:
		return managev1.CampaignRecipientScope_CAMPAIGN_RECIPIENT_SCOPE_ALL_MATCHING_USERS, nil
	default:
		return 0, fmt.Errorf("unsupported campaign recipient scope %q", scope)
	}
}

// campaignStatusToProto maps a stored Campaign status to the API enum. A
// value the API does not know is UNSPECIFIED.
func campaignStatusToProto(status CampaignStatus) managev1.CampaignStatus {
	switch status {
	case CampaignStatusDraft:
		return managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT
	case CampaignStatusScheduled:
		return managev1.CampaignStatus_CAMPAIGN_STATUS_SCHEDULED
	case CampaignStatusSending:
		return managev1.CampaignStatus_CAMPAIGN_STATUS_SENDING
	case CampaignStatusSent:
		return managev1.CampaignStatus_CAMPAIGN_STATUS_SENT
	case CampaignStatusFailed:
		return managev1.CampaignStatus_CAMPAIGN_STATUS_FAILED
	default:
		return managev1.CampaignStatus_CAMPAIGN_STATUS_UNSPECIFIED
	}
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
