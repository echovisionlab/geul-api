package accountadapter

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/account"
	memberdomain "github.com/echovisionlab/geul-api/internal/member"
	"gorm.io/gorm"
)

// MemberEmailProjection adapts Member-owned email projection operations for
// the Account consumer port. It intentionally does not import any service.
type MemberEmailProjection struct{}

var _ account.MemberEmailProjection = MemberEmailProjection{}

func (MemberEmailProjection) PrimaryEmail(
	ctx context.Context, db *gorm.DB, memberID, identityID string,
) (string, error) {
	return memberdomain.PrimaryEmail(ctx, db, memberID, identityID)
}

func (MemberEmailProjection) SyncEmailProjection(
	ctx context.Context, db *gorm.DB, memberID, identityID, primaryEmail string, availableEmails []string,
) error {
	return memberdomain.SyncEmailProjection(ctx, db, memberID, identityID, primaryEmail, availableEmails)
}
