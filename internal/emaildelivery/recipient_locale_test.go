package emaildelivery

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestResolveEmailRecipientLocaleNormalizesUserLocale(t *testing.T) {
	require.Equal(t, "pt-BR", resolveEmailRecipientLocale("pt"))
	require.Empty(t, resolveEmailRecipientLocale("unsupported"))
}

func TestLookupEmailRecipientLocaleUsesExactActiveMember(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
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

	require.Equal(t, "pt-BR", LookupEmailRecipientLocale(t.Context(), db, " Reader@Example.Test "))

	require.NoError(t, db.Exec("UPDATE kratos.identities SET external_id = 'member-other'").Error)
	require.Empty(t, LookupEmailRecipientLocale(t.Context(), db, "reader@example.test"))

	require.NoError(t, db.Exec("UPDATE kratos.identities SET external_id = 'member-1', state = 'inactive'").Error)
	require.Empty(t, LookupEmailRecipientLocale(t.Context(), db, "reader@example.test"))
}
