package memberadapter

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/identityread"
)

// DashboardMembers counts Members for the administrator dashboard.
type DashboardMembers struct{}

// CountActive counts the live Members whose bilaterally linked Identity is
// active and not banned.
func (DashboardMembers) CountActive(ctx context.Context, db *gorm.DB) (int64, error) {
	var count int64
	err := db.WithContext(ctx).Raw(`
		SELECT COUNT(*)
		FROM member
		` + identityread.JoinLinked("member", "identity") + `
		WHERE member.deleted_at IS NULL
		  AND NOT ` + identityread.BannedSQL("identity")).Scan(&count).Error
	return count, err
}
