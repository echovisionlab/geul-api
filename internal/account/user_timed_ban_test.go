package account

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestHasPendingAccountDeletionUsesIdentityAuthority(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE user_deletion_request (
			id TEXT PRIMARY KEY,
			member_id TEXT NOT NULL,
			identity_id TEXT NOT NULL,
			lifecycle_state TEXT NOT NULL
		)
	`).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO user_deletion_request (id, member_id, identity_id, lifecycle_state)
		VALUES ('request-1', 'member-1', 'identity-1', 'scheduled')
	`).Error)

	pending, err := hasPendingAccountDeletion(db, "identity-1")
	require.NoError(t, err)
	require.True(t, pending)

	pending, err = hasPendingAccountDeletion(db, "identity-2")
	require.NoError(t, err)
	require.False(t, pending)
}
