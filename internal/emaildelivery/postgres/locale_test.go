package emaildeliverypostgres_test

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	emaildeliverypostgres "github.com/echovisionlab/geul-api/internal/emaildelivery/postgres"
	"github.com/echovisionlab/geul-api/internal/testutil/sqlitetest"
)

// openLocaleDB is a Member linked to an active Identity in a SQLite database
// that attaches a kratos schema, as the identityread fragments allow.
func openLocaleDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := sqlitetest.Open(t, sqlite.Open, &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec("ATTACH DATABASE ':memory:' AS kratos").Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE member (
			id TEXT PRIMARY KEY,
			account_identity_id TEXT,
			primary_email TEXT,
			preferred_locale TEXT,
			deleted_at DATETIME
		);
		CREATE TABLE kratos.identities (
			id TEXT PRIMARY KEY,
			external_id TEXT,
			state TEXT
		)
	`).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO member (id, account_identity_id, primary_email, preferred_locale)
		VALUES ('member-1', 'identity-1', 'reader@example.test', 'pt');
		INSERT INTO kratos.identities (id, external_id, state)
		VALUES ('identity-1', 'member-1', 'active')
	`).Error)
	return db
}

func TestRecipientLocaleUsesTheExactActiveMember(t *testing.T) {
	db := openLocaleDB(t)
	store := emaildeliverypostgres.NewStore(db)

	locale, err := store.RecipientLocale(t.Context(), "reader@example.test")
	require.NoError(t, err)
	require.Equal(t, "pt", locale)
	locale, err = store.RecipientLocale(t.Context(), "other@example.test")
	require.NoError(t, err)
	require.Empty(t, locale)

	require.NoError(t, db.Exec("UPDATE kratos.identities SET external_id = 'member-other'").Error)
	locale, err = store.RecipientLocale(t.Context(), "reader@example.test")
	require.NoError(t, err)
	require.Empty(t, locale, "the link must be bilateral")

	require.NoError(t, db.Exec("UPDATE kratos.identities SET external_id = 'member-1', state = 'inactive'").Error)
	locale, err = store.RecipientLocale(t.Context(), "reader@example.test")
	require.NoError(t, err)
	require.Empty(t, locale)

	require.NoError(t, db.Exec("UPDATE kratos.identities SET state = 'active'").Error)
	require.NoError(t, db.Exec("UPDATE member SET deleted_at = CURRENT_TIMESTAMP").Error)
	locale, err = store.RecipientLocale(t.Context(), "reader@example.test")
	require.NoError(t, err)
	require.Empty(t, locale)
}

func TestIdentityLocaleUsesTheLinkedActiveMember(t *testing.T) {
	db := openLocaleDB(t)
	store := emaildeliverypostgres.NewStore(db)

	locale, err := store.IdentityLocale(t.Context(), "identity-1")
	require.NoError(t, err)
	require.Equal(t, "pt", locale)

	require.NoError(t, db.Exec("UPDATE member SET preferred_locale = ''").Error)
	locale, err = store.IdentityLocale(t.Context(), "identity-1")
	require.NoError(t, err)
	require.Empty(t, locale, "an empty stored locale is none")

	require.NoError(t, db.Exec("UPDATE member SET preferred_locale = 'ko'").Error)
	require.NoError(t, db.Exec("UPDATE kratos.identities SET state = 'inactive'").Error)
	locale, err = store.IdentityLocale(t.Context(), "identity-1")
	require.NoError(t, err)
	require.Empty(t, locale)
}
