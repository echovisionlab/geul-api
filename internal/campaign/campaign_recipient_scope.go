package campaign

import (
	"fmt"
	"strings"
)

const (
	campaignRecipientScopeSubscribedUsers  = "SUBSCRIBED_USERS"
	campaignRecipientScopeAllMatchingUsers = "ALL_MATCHING_USERS"
)

func campaignRecipientScopeValue(scope RecipientScope) (string, error) {
	switch scope {
	case RecipientScopeSubscribedUsers:
		return campaignRecipientScopeSubscribedUsers, nil
	case RecipientScopeAllMatchingUsers:
		return campaignRecipientScopeAllMatchingUsers, nil
	default:
		return "", fmt.Errorf("unsupported campaign recipient scope")
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
