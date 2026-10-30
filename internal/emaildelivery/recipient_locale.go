package emaildelivery

import (
	"context"
	"strings"

	emailutil "github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/localization"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

// LookupEmailRecipientLocale resolves the preferred locale from the exact
// active Member whose selected delivery address matches the recipient.
func LookupEmailRecipientLocale(ctx context.Context, db *gorm.DB, recipient string) string {
	recipient = strings.TrimSpace(recipient)
	if recipient == "" || db == nil {
		return ""
	}

	return resolveEmailRecipientLocale(lookupEmailUserLocale(ctx, db, recipient))
}

func resolveEmailRecipientLocale(userLocale string) string {
	if normalized := localization.NormalizeSupportedLocale(userLocale); normalized != nil {
		return *normalized
	}
	return ""
}

func lookupEmailUserLocale(ctx context.Context, db *gorm.DB, recipient string) string {
	recipient = emailutil.NormalizeAddressForDelivery(recipient)
	if recipient == "" {
		return ""
	}
	var row struct {
		PreferredLocale *string `gorm:"column:preferred_locale"`
	}
	err := db.WithContext(ctx).
		Table("member AS m").
		Joins("JOIN kratos.identities AS ki ON ki.id = m.account_identity_id AND ki.external_id = CAST(m.id AS text)").
		Select("NULLIF(m.preferred_locale, '') AS preferred_locale").
		Where("ki.state = 'active' AND m.deleted_at IS NULL AND m.primary_email = ?", recipient).
		Limit(1).
		Scan(&row).Error
	if err != nil {
		LogDeliveryFailure(ctx, "mail.delivery.locale_lookup_failed", &managev1.SendEmailEvent{Recipient: recipient}, err)
		return ""
	}
	if row.PreferredLocale == nil {
		return ""
	}
	return resolveEmailRecipientLocale(*row.PreferredLocale)
}

// LookupEmailIdentityLocale resolves the preferred locale of the active Member
// linked to identityID. A verification email for a pending address cannot be
// matched by recipient, because no Member uses that address yet.
func LookupEmailIdentityLocale(ctx context.Context, db *gorm.DB, identityID string) string {
	identityID = strings.TrimSpace(identityID)
	if identityID == "" || db == nil {
		return ""
	}
	var row struct {
		PreferredLocale *string `gorm:"column:preferred_locale"`
	}
	err := db.WithContext(ctx).
		Table("member AS m").
		Joins("JOIN kratos.identities AS ki ON ki.id = m.account_identity_id AND ki.external_id = CAST(m.id AS text)").
		Select("NULLIF(m.preferred_locale, '') AS preferred_locale").
		Where("ki.state = 'active' AND m.deleted_at IS NULL AND m.account_identity_id = ?", identityID).
		Limit(1).
		Scan(&row).Error
	if err != nil {
		LogDeliveryFailure(ctx, "mail.delivery.locale_lookup_failed", &managev1.SendEmailEvent{}, err)
		return ""
	}
	if row.PreferredLocale == nil {
		return ""
	}
	return resolveEmailRecipientLocale(*row.PreferredLocale)
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
