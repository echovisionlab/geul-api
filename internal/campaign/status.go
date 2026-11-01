package campaign

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
