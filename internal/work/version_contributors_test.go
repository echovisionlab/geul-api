package work

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newVersionContributorUnitDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE member (
			id TEXT PRIMARY KEY,
			nickname TEXT NOT NULL,
			onboarded BOOLEAN NOT NULL,
			deleted_at DATETIME
		)
	`).Error)
	return db
}

func TestResolveVersionContributorNamesIncludesActiveAndTombstoneMembers(t *testing.T) {
	db := newVersionContributorUnitDB(t)
	activeID := uuid.NewString()
	tombstoneID := uuid.NewString()
	require.NoError(t, db.Exec(`
		INSERT INTO member (id, nickname, onboarded, deleted_at)
		VALUES (?, 'Active', TRUE, NULL), (?, 'Preserved', TRUE, ?)
	`, activeID, tombstoneID, time.Now().UTC()).Error)

	names, err := resolveVersionContributorNames(t.Context(), db, []string{tombstoneID, activeID, activeID})
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		activeID:    "Active",
		tombstoneID: "Preserved",
	}, names)
}

func TestResolveVersionContributorNamesIncludesUnonboardedMemberAndRejectsMissingMember(t *testing.T) {
	db := newVersionContributorUnitDB(t)
	unonboardedID := uuid.NewString()
	require.NoError(t, db.Exec(`
		INSERT INTO member (id, nickname, onboarded, deleted_at)
		VALUES (?, ?, FALSE, NULL)
	`, unonboardedID, unonboardedID).Error)

	names, err := resolveVersionContributorNames(t.Context(), db, []string{unonboardedID})
	require.NoError(t, err)
	require.Equal(t, map[string]string{unonboardedID: unonboardedID}, names)

	_, err = resolveVersionContributorNames(t.Context(), db, []string{uuid.NewString()})
	require.ErrorContains(t, err, "Member set is incomplete")
}

func TestResolveVersionContributorNamesDoesNotHideDatabaseFailure(t *testing.T) {
	db := newVersionContributorUnitDB(t)
	require.NoError(t, db.Exec("DROP TABLE member").Error)

	_, err := resolveVersionContributorNames(t.Context(), db, []string{uuid.NewString()})
	require.ErrorContains(t, err, "resolve work Version contributor Members")
}
