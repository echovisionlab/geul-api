package email

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
)

var (
	// ErrTestRecipientSuppressed means the worker would drop a test email to
	// this address because a bounce, complaint or administrator suppressed it.
	ErrTestRecipientSuppressed = errors.New("test email recipient is suppressed")
	// ErrNoActiveMailAdapter means no adapter could send the test email.
	ErrNoActiveMailAdapter = errors.New("no active mail adapter")
)

// CheckTestSendReadiness reports, before a test email is queued, the facts
// that would make the worker drop or hold it. A queued test otherwise looks
// successful to the administrator even when nothing is sent.
func CheckTestSendReadiness(ctx context.Context, db *gorm.DB, recipient string) error {
	if normalized := NormalizeAddressForDelivery(recipient); normalized != "" {
		var suppressed int64
		if err := db.WithContext(ctx).Model(&model.EmailSuppression{}).
			Where("LOWER(email) = ? AND released_at IS NULL", normalized).
			Count(&suppressed).Error; err != nil {
			return err
		}
		if suppressed > 0 {
			return ErrTestRecipientSuppressed
		}
	}
	var active int64
	if err := db.WithContext(ctx).Model(&model.MailAdapter{}).
		Where("is_active = ?", true).
		Count(&active).Error; err != nil {
		return err
	}
	if active == 0 {
		return ErrNoActiveMailAdapter
	}
	return nil
}

// TestSendReadinessMessage is the administrator-facing reason for err, or ""
// when err is not a readiness failure.
func TestSendReadinessMessage(err error) string {
	switch {
	case errors.Is(err, ErrTestRecipientSuppressed):
		return "This address is on the email suppression list after a bounce or complaint, so the test email would not be sent. Release it in Email suppressions first."
	case errors.Is(err, ErrNoActiveMailAdapter):
		return "No mail adapter is active, so the test email would not be sent. Activate one in Mail adapters first."
	default:
		return ""
	}
}
