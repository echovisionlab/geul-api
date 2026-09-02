package campaign

import (
	"fmt"
	"strings"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const (
	campaignRecipientScopeSubscribedUsers  = "SUBSCRIBED_USERS"
	campaignRecipientScopeAllMatchingUsers = "ALL_MATCHING_USERS"
)

func campaignRecipientScopeFromProto(scope managev1.CampaignRecipientScope) (string, error) {
	switch scope {
	case managev1.CampaignRecipientScope_CAMPAIGN_RECIPIENT_SCOPE_SUBSCRIBED_USERS:
		return campaignRecipientScopeSubscribedUsers, nil
	case managev1.CampaignRecipientScope_CAMPAIGN_RECIPIENT_SCOPE_ALL_MATCHING_USERS:
		return campaignRecipientScopeAllMatchingUsers, nil
	default:
		return "", fmt.Errorf("unsupported campaign recipient scope")
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

func validateCampaignRecipientScope(scope string) error {
	switch strings.TrimSpace(scope) {
	case campaignRecipientScopeSubscribedUsers, campaignRecipientScopeAllMatchingUsers:
		return nil
	default:
		return fmt.Errorf("unsupported campaign recipient scope %q", scope)
	}
}
