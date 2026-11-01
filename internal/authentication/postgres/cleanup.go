package authenticationpostgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// CleanupExpiredAuthCodeIssuances physically removes a bounded batch of
// logically expired limiter rows. It is safe to run alongside Reserve because
// active budget queries ignore expired windows and the delete targets only
// expires_at <= now.
func CleanupExpiredAuthCodeIssuances(ctx context.Context, db *gorm.DB, now time.Time, limit int) (int64, error) {
	if db == nil {
		return 0, errors.New("auth code issuance cleanup database is required")
	}
	if limit <= 0 {
		return 0, errors.New("auth code issuance cleanup limit must be positive")
	}
	result := db.WithContext(ctx).Exec(`
		WITH expired AS (
			SELECT token
			FROM public.auth_code_issuance
			WHERE expires_at <= ?
			ORDER BY expires_at, token
			LIMIT ?
		)
		DELETE FROM public.auth_code_issuance AS issuance
		USING expired
		WHERE issuance.token = expired.token`, now.UTC(), limit)
	if result.Error != nil {
		return 0, fmt.Errorf("delete expired auth code issuances: %w", result.Error)
	}
	return result.RowsAffected, nil
}
