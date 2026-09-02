package account

import (
	"context"

	"gorm.io/gorm"
)

// memberEmailProjectionStub isolates Account candidate and skip-reason tests
// from Member persistence; Member's real lock/read/update behavior is covered
// by its PostgreSQL integration tests.
type memberEmailProjectionStub struct {
	memberID   string
	identityID string
	primary    string
	err        error
}

func (s memberEmailProjectionStub) PrimaryEmail(_ context.Context, _ *gorm.DB, memberID, identityID string) (string, error) {
	if (s.memberID != "" && memberID != s.memberID) || (s.identityID != "" && identityID != s.identityID) {
		return "", gorm.ErrRecordNotFound
	}
	return s.primary, s.err
}

func (s memberEmailProjectionStub) SyncEmailProjection(context.Context, *gorm.DB, string, string, string, []string) error {
	return s.err
}
