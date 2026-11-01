package emaildelivery

import (
	"context"
	"strings"

	emailutil "github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/localization"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// LookupEmailRecipientLocale resolves the preferred locale from the exact
// active Member whose selected delivery address matches the recipient.
func LookupEmailRecipientLocale(ctx context.Context, store Store, recipient string) string {
	recipient = emailutil.NormalizeAddressForDelivery(recipient)
	if recipient == "" {
		return ""
	}
	locale, err := store.RecipientLocale(ctx, recipient)
	if err != nil {
		LogDeliveryFailure(ctx, "mail.delivery.locale_lookup_failed", &managev1.SendEmailEvent{Recipient: recipient}, err)
		return ""
	}
	return resolveEmailRecipientLocale(locale)
}

func resolveEmailRecipientLocale(userLocale string) string {
	if normalized := localization.NormalizeSupportedLocale(userLocale); normalized != nil {
		return *normalized
	}
	return ""
}

// LookupEmailIdentityLocale resolves the preferred locale of the active Member
// linked to identityID. A verification email for a pending address cannot be
// matched by recipient, because no Member uses that address yet.
func LookupEmailIdentityLocale(ctx context.Context, store Store, identityID string) string {
	identityID = strings.TrimSpace(identityID)
	if identityID == "" {
		return ""
	}
	locale, err := store.IdentityLocale(ctx, identityID)
	if err != nil {
		LogDeliveryFailure(ctx, "mail.delivery.locale_lookup_failed", &managev1.SendEmailEvent{}, err)
		return ""
	}
	return resolveEmailRecipientLocale(locale)
}

// EmailRecipientContextIdentityID is the Account identity an authentication
// email belongs to, or "" for other recipient contexts.
func EmailRecipientContextIdentityID(job *managev1.SendEmailEvent) string {
	if verification := job.GetAccountVerification(); verification != nil {
		return verification.GetIdentityId()
	}
	if login := job.GetAuthLogin(); login != nil {
		return login.GetIdentityId()
	}
	return ""
}
