package accountadapter

import (
	"context"
	"errors"

	"gorm.io/gorm"

	accountpostgres "github.com/echovisionlab/geul-api/internal/account/postgres"
	memberdomain "github.com/echovisionlab/geul-api/internal/member"
	memberpostgres "github.com/echovisionlab/geul-api/internal/member/postgres"
)

// MemberEmailProjection adapts Member-owned email projection operations for
// the Account consumer port. It intentionally does not import any service.
type MemberEmailProjection struct{}

var _ accountpostgres.MemberEmails = MemberEmailProjection{}

func (MemberEmailProjection) PrimaryEmail(
	ctx context.Context, db *gorm.DB, memberID, identityID string,
) (string, error) {
	email, err := memberdomain.PrimaryEmail(ctx, memberpostgres.Bind(db), memberID, identityID)
	if errors.Is(err, memberdomain.ErrMemberNotFound) {
		return "", gorm.ErrRecordNotFound
	}
	return email, err
}

func (MemberEmailProjection) SyncEmailProjection(
	ctx context.Context, db *gorm.DB, memberID, identityID, primaryEmail string, availableEmails []string,
) error {
	err := memberdomain.SyncEmailProjection(ctx, memberpostgres.Bind(db), memberID, identityID, primaryEmail, availableEmails)
	if errors.Is(err, memberdomain.ErrMemberNotFound) {
		return gorm.ErrRecordNotFound
	}
	return err
}
