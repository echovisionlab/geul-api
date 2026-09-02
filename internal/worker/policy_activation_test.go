package worker

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestScheduledLegalActivationFailsClosedOnMultiplePendingVersionsUnit(
	t *testing.T,
) {
	t.Run("privacy", func(t *testing.T) {
		db := newPolicyActivationUnitDB(t)
		now := time.Now().UTC()
		for version := 1; version <= 2; version++ {
			require.NoError(t, db.Exec(
				`INSERT INTO privacy_history (
					id, version, status, effective_from
				) VALUES (?, ?, ?, ?)`,
				uuid.NewString(),
				version,
				managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED.String(),
				now,
			).Error)
		}
		require.ErrorContains(
			t,
			(&Handlers{db: db}).handleActivatePrivacy(t.Context()),
			"multiple scheduled privacy versions",
		)
	})

	t.Run("terms", func(t *testing.T) {
		db := newPolicyActivationUnitDB(t)
		now := time.Now().UTC()
		for version := 1; version <= 2; version++ {
			require.NoError(t, db.Exec(
				`INSERT INTO terms_history (
					id, version, status, effective_from
				) VALUES (?, ?, ?, ?)`,
				uuid.NewString(),
				version,
				managev1.TermsStatus_TERMS_STATUS_SCHEDULED.String(),
				now,
			).Error)
		}
		require.ErrorContains(
			t,
			(&Handlers{db: db}).handleActivateTerms(t.Context()),
			"multiple scheduled terms versions",
		)
	})
}

func newPolicyActivationUnitDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE privacy_history (
			id text PRIMARY KEY,
			version integer NOT NULL,
			status text NOT NULL,
			effective_from datetime
		)
	`).Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE terms_history (
			id text PRIMARY KEY,
			version integer NOT NULL,
			status text NOT NULL,
			effective_from datetime
		)
	`).Error)
	return db
}
