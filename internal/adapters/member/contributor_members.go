package memberadapter

import (
	"context"

	campaignpostgres "github.com/echovisionlab/geul-api/internal/campaign/postgres"
	emailauthoringpostgres "github.com/echovisionlab/geul-api/internal/emailauthoring/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ContributorMembers locks the Member rows a collaboration write credits.
type ContributorMembers struct{}

var (
	_ campaignpostgres.ContributorMembers = ContributorMembers{}
	_ emailauthoringpostgres.Members      = ContributorMembers{}
)

// LockMembers takes a KEY SHARE lock on each existing Member of memberIDs,
// so a concurrent Member deletion waits for the write, and returns the IDs
// it locked.
func (ContributorMembers) LockMembers(ctx context.Context, tx *gorm.DB, memberIDs []string) ([]string, error) {
	locked := []string{}
	err := tx.WithContext(ctx).
		Table("member").
		Clauses(clause.Locking{Strength: "KEY SHARE"}).
		Where("id IN ?", memberIDs).
		Pluck("id::text", &locked).Error
	return locked, err
}
