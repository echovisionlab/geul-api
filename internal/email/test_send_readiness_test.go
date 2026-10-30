package email

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil/sqlitetest"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestCheckTestSendReadinessReportsDroppedTestEmails(t *testing.T) {
	t.Parallel()
	db, err := sqlitetest.Open(t, sqlite.Open, &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE mail_adapter (id TEXT PRIMARY KEY, is_active BOOLEAN NOT NULL)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE email_suppression (id TEXT PRIMARY KEY, email TEXT NOT NULL, released_at DATETIME)`).Error)
	ctx := t.Context()

	require.ErrorIs(t, CheckTestSendReadiness(ctx, db, "admin@example.test"), ErrNoActiveMailAdapter)
	require.NoError(t, db.Exec(`INSERT INTO mail_adapter (id, is_active) VALUES ('ses', TRUE)`).Error)
	require.NoError(t, CheckTestSendReadiness(ctx, db, "admin@example.test"))

	require.NoError(t, db.Exec(`INSERT INTO email_suppression (id, email) VALUES ('s1', 'bounced@example.test')`).Error)
	err = CheckTestSendReadiness(ctx, db, " Bounced@Example.test ")
	require.ErrorIs(t, err, ErrTestRecipientSuppressed)
	require.Contains(t, TestSendReadinessMessage(err), "suppression list")

	require.NoError(t, db.Exec(`UPDATE email_suppression SET released_at = CURRENT_TIMESTAMP`).Error)
	require.NoError(t, CheckTestSendReadiness(ctx, db, "bounced@example.test"), "a released suppression no longer blocks")
}
