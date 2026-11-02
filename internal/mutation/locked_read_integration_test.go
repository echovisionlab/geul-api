//go:build integration

package mutation

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// A projection that share-locks its root cannot run in Read's read-only
// transaction; the locked reads accept the lock and still write nothing.
func TestLockedReadsAcceptRowLocksIntegration(t *testing.T) {
	t.Parallel()
	db := orytest.SetupOryStack(t).DB
	lockSettings := func(strength string) func(*gorm.DB) error {
		return func(tx *gorm.DB) error {
			var id int
			return tx.Table("site_settings").Select("id").Where("id = ?", 1).
				Clauses(clause.Locking{Strength: strength}).Take(&id).Error
		}
	}

	require.ErrorContains(t, Read(t.Context(), db, lockSettings(clause.LockingStrengthShare)), "read-only transaction")
	require.NoError(t, ReadLocked(t.Context(), db, lockSettings(clause.LockingStrengthShare)))
	require.NoError(t, ReadCurrent(t.Context(), db, lockSettings(clause.LockingStrengthUpdate)))
}
