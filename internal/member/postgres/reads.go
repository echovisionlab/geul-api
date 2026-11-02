package memberpostgres

import (
	"context"
	"fmt"
	"time"

	"github.com/echovisionlab/geul-api/internal/memberref"

	memberdomain "github.com/echovisionlab/geul-api/internal/member"
	"gorm.io/gorm"
)

const memberEmailRetentionCleanupBatchSize = 500

// LoadSummaries reads plain Member references on the caller's database handle.
func LoadSummaries(ctx context.Context, db *gorm.DB, cdnDomain string, memberIDs []string) (map[string]*memberref.Summary, error) {
	values, err := memberdomain.LoadSummaries(ctx, store{tx: db, ports: Ports{CDNDomain: cdnDomain}}, memberIDs)
	if err != nil {
		return nil, err
	}
	result := make(map[string]*memberref.Summary, len(values))
	for id, value := range values {
		result[id] = &value
	}
	return result, nil
}

// LoadAuthorizationEligibleSummary reads an active, onboarded Member that may hold authority.
func LoadAuthorizationEligibleSummary(ctx context.Context, db *gorm.DB, cdnDomain, memberID string) (*memberref.Summary, error) {
	summary, err := memberdomain.LoadAuthorizationEligibleSummary(ctx, store{tx: db, ports: Ports{CDNDomain: cdnDomain}}, memberID)
	if err != nil {
		return nil, err
	}
	return &summary, nil
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
	normalized, ok := memberdomain.NormalizeEmailInput(email)
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
