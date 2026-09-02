package emaildelivery

import (
	"context"
	"strings"

	emailutil "github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/localization"
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
	if err != nil || row.PreferredLocale == nil {
		return ""
	}
	return resolveEmailRecipientLocale(*row.PreferredLocale)
}
