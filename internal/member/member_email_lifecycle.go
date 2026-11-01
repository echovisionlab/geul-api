package member

import (
	"context"
	"fmt"
	"net/mail"
	"strings"

	emailutil "github.com/echovisionlab/geul-api/internal/email"
	"gorm.io/gorm"
)

// RegistrationEmailReuseBlocked reports whether a deleted or unlinked
// Member still holds email. It stays on gorm for the registration reuse-hold
// adapter of Authentication, which calls it with its pool; the Member Store
// reads the same predicate through it.
func RegistrationEmailReuseBlocked(ctx context.Context, db *gorm.DB, email string) (bool, error) {
	if db == nil {
		return false, fmt.Errorf("member database is required")
	}
	normalized, ok := NormalizeEmailInput(email)
	if !ok {
		return false, nil
	}
	var blocked bool
	if err := db.WithContext(ctx).Raw(`
		SELECT EXISTS (
			SELECT 1
			FROM member
			WHERE account_identity_id IS NULL
			  AND available_emails @> ARRAY[?]::text[]
			  AND (
				deleted_at IS NULL
				OR deleted_at > CURRENT_TIMESTAMP - interval '7 days'
			  )
		)
	`, normalized).Scan(&blocked).Error; err != nil {
		return false, err
	}
	return blocked, nil
}

// NormalizeEmailInput validates and canonicalizes a registration email.
func NormalizeEmailInput(email string) (string, bool) {
	normalized := emailutil.NormalizeAddressForDelivery(email)
	if normalized == "" || strings.HasSuffix(normalized, ".local") {
		return normalized, false
	}
	parsed, err := mail.ParseAddress(normalized)
	if err != nil {
		return normalized, false
	}
	return emailutil.NormalizeAddressForDelivery(parsed.Address), true
}
