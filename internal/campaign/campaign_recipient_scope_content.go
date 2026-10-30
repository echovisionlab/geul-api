package campaign

import (
	"regexp"

	"github.com/echovisionlab/geul-api/internal/email"
	errs "github.com/echovisionlab/geul-api/internal/errors"
)

var unsubscribeLinkPlaceholderRe = regexp.MustCompile(`(?i){{\s*unsubscribe_link\s*}}`)

// validateCampaignRecipientScopeContent rejects an unsubscribe link in a
// Campaign sent to every matching member. Those members receive it whether or
// not they subscribed, so the link would promise an opt-out that never applies.
func validateCampaignRecipientScopeContent(scope string, snapshot CampaignDeliverySnapshot) error {
	if scope != campaignRecipientScopeAllMatchingUsers || !campaignSnapshotUsesUnsubscribeLink(snapshot) {
		return nil
	}
	return errs.FailedPrecondition(
		"Campaigns sent to all matching members can't include {{unsubscribe_link}}. Send to subscribers only, or remove the link.",
	)
}

func campaignSnapshotUsesUnsubscribeLink(snapshot CampaignDeliverySnapshot) bool {
	sources := []string{snapshot.Subject, snapshot.ContentHTML}
	for _, translation := range snapshot.Translations {
		sources = append(sources, translation.Subject, translation.ContentHTML)
	}
	if snapshot.LayoutTranslations != nil {
		for _, layout := range *snapshot.LayoutTranslations {
			sources = append(sources, layout.HTMLContent)
		}
	}
	for _, source := range sources {
		if unsubscribeLinkPlaceholderRe.MatchString(email.NormalizeTemplatePlaceholders(source)) {
			return true
		}
	}
	return false
}
