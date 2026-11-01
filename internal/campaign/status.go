package campaign

import (
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// Client messages of Campaign lifecycle rules.
const (
	msgCampaignCannotUpdateSent    = "only draft campaigns can be edited"
	msgCampaignOnlyScheduleDraft   = "only draft campaigns can be scheduled"
	msgCampaignNeedsContent        = "campaign must have content before scheduling"
	msgCampaignOnlyCancelScheduled = "can only cancel scheduled campaigns"
	msgCampaignAlreadySent         = "only draft campaigns can be sent"
	msgCampaignNoRecipients        = "no eligible recipients to send to"
	msgCampaignSegmentNotFound     = "campaign segment not found"
)

// campaignStatusToProto maps a stored Campaign status to the API enum. A
// value the API does not know is UNSPECIFIED.
func campaignStatusToProto(status model.CampaignStatus) managev1.CampaignStatus {
	switch status {
	case model.CampaignStatusDraft:
		return managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT
	case model.CampaignStatusScheduled:
		return managev1.CampaignStatus_CAMPAIGN_STATUS_SCHEDULED
	case model.CampaignStatusSending:
		return managev1.CampaignStatus_CAMPAIGN_STATUS_SENDING
	case model.CampaignStatusSent:
		return managev1.CampaignStatus_CAMPAIGN_STATUS_SENT
	case model.CampaignStatusFailed:
		return managev1.CampaignStatus_CAMPAIGN_STATUS_FAILED
	default:
		return managev1.CampaignStatus_CAMPAIGN_STATUS_UNSPECIFIED
	}
}
