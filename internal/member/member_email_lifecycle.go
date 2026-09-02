package member

import (
	"context"
	"fmt"
	"net/mail"
	"strings"
	"time"

	emailutil "github.com/echovisionlab/geul-api/internal/email"
	"gorm.io/gorm"
)

const memberEmailRetentionCleanupBatchSize = 500

func RegistrationEmailReuseBlocked(ctx context.Context, db *gorm.DB, email string) (bool, error) {
	if db == nil {
		return false, fmt.Errorf("member database is required")
	}
	normalized, ok := normalizeMemberEmailInput(email)
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

// PublicEmailCodeRegistrationBlocked additionally protects an address that is
// already projected by a current Identity but has no Email Code identifier.
// The public controller uses this only after credential lookup found no login
// identity, preserving one anti-enumeration response while requiring the user
// to social-login and prove the mailbox from Security. SSO registration does
// not use this check because provider email strings are not globally unique.
func PublicEmailCodeRegistrationBlocked(ctx context.Context, db *gorm.DB, email string) (bool, error) {
	if db == nil {
		return false, fmt.Errorf("member database is required")
	}
	normalized, ok := normalizeMemberEmailInput(email)
	if !ok {
		return false, nil
	}
	var blocked bool
	if err := db.WithContext(ctx).Raw(`
		SELECT EXISTS (
			SELECT 1
			FROM member
			WHERE available_emails @> ARRAY[?]::text[]
			  AND (
				account_identity_id IS NOT NULL
				OR deleted_at IS NULL
				OR deleted_at > CURRENT_TIMESTAMP - interval '7 days'
			  )
		)
	`, normalized).Scan(&blocked).Error; err != nil {
		return false, err
	}
	return blocked, nil
}

func normalizeMemberEmailInput(email string) (string, bool) {
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

// ScrubExpiredMemberEmailProjections removes only the bounded private email
// projection after two years. It stores no scheduler or retry state.
func ScrubExpiredMemberEmailProjections(ctx context.Context, db *gorm.DB, now time.Time) (int64, error) {
	if db == nil {
		return 0, fmt.Errorf("member database is required")
	}
	result := db.WithContext(ctx).Exec(`
		UPDATE member
		SET primary_email = NULL,
		    available_emails = '{}'::text[],
		    updated_at = ?
		WHERE id IN (
			SELECT id
			FROM member
			WHERE account_identity_id IS NULL
			  AND deleted_at IS NOT NULL
			  AND deleted_at <= ?::timestamptz - interval '2 years'
			  AND (primary_email IS NOT NULL OR cardinality(available_emails) > 0)
			ORDER BY deleted_at, id
			LIMIT ?
			FOR UPDATE SKIP LOCKED
		)
	`, now.UTC(), now.UTC(), memberEmailRetentionCleanupBatchSize)
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}
