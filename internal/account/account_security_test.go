package account

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/structured"
)

type accountSecurityIdentityManager struct {
	accountIdentityManager
	identity *auth.Identity
}

func (m accountSecurityIdentityManager) GetIdentityWithIncludeCredential(
	_ context.Context,
	identityID string,
	_ string,
) (*auth.Identity, error) {
	if m.identity == nil || m.identity.ID != identityID {
		return nil, gorm.ErrRecordNotFound
	}
	return m.identity, nil
}

func openAccountSecuritySQLite(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec("ATTACH DATABASE ':memory:' AS kratos").Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE member (
			id TEXT PRIMARY KEY,
			account_identity_id TEXT,
			primary_email TEXT,
			deleted_at DATETIME
		)
	`).Error)
	require.NoError(t, db.Exec(`
		CREATE TABLE kratos.sessions (
			id TEXT PRIMARY KEY,
			identity_id TEXT,
			active BOOLEAN,
			authenticated_at DATETIME,
			expires_at DATETIME
		)
	`).Error)
	return db
}

func TestDeliveryPrimaryEmailForIdentityReadsExactActiveBilateralMember(t *testing.T) {
	db := openAccountSecuritySQLite(t)
	require.NoError(t, db.Exec(`
		INSERT INTO member (id, account_identity_id, primary_email, deleted_at)
		VALUES ('member-1', 'identity-1', 'selected@example.test', NULL)
	`).Error)

	service := &AccountService{db: db, memberEmails: memberEmailProjectionStub{memberID: "member-1", identityID: "identity-1", primary: "selected@example.test"}}
	email, err := service.deliveryPrimaryEmailForIdentity(t.Context(), "member-1", "identity-1")
	require.NoError(t, err)
	require.Equal(t, "selected@example.test", email)

	_, err = service.deliveryPrimaryEmailForIdentity(t.Context(), "member-1", "identity-other")
	require.Error(t, err)
	require.NoError(t, db.Exec("UPDATE member SET deleted_at = CURRENT_TIMESTAMP WHERE id = 'member-1'").Error)
	service.memberEmails = memberEmailProjectionStub{err: gorm.ErrRecordNotFound}
	_, err = service.deliveryPrimaryEmailForIdentity(t.Context(), "member-1", "identity-1")
	require.Error(t, err)
}

func TestSecurityForIdentityRejectsNonCanonicalMemberPrimaryProjection(t *testing.T) {
	db := openAccountSecuritySQLite(t)
	memberID := uuid.NewString()
	identityID := uuid.NewString()
	sessionID := uuid.NewString()
	canonicalEmail := "canonical@example.test"
	selectedEmail := "selected@example.test"
	require.NoError(t, db.Exec(
		"INSERT INTO member (id, account_identity_id, primary_email) VALUES (?, ?, ?)",
		memberID,
		identityID,
		selectedEmail,
	).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO kratos.sessions (id, identity_id, active, authenticated_at, expires_at) VALUES (?, ?, ?, ?, ?)",
		sessionID,
		identityID,
		true,
		time.Now().UTC().Add(-time.Minute),
		time.Now().UTC().Add(time.Hour),
	).Error)
	identity := &auth.Identity{
		ID: identityID, ExternalID: memberID, State: auth.KratosStateActive,
		Traits: structured.Fields{"email": canonicalEmail},
		VerifiableAddresses: []auth.VerifiableAddress{
			{Via: "email", Value: canonicalEmail, Verified: true},
			{Via: "email", Value: selectedEmail, Verified: true},
		},
	}
	service := &AccountService{
		db:           db,
		identity:     accountSecurityIdentityManager{identity: identity},
		memberEmails: memberEmailProjectionStub{primary: selectedEmail},
	}

	_, err := service.securityForIdentity(t.Context(), memberID, identityID, sessionID)
	require.ErrorContains(t, err, "primary email is not synchronized")
}
