//go:build integration

package member

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func seedActiveMemberEmailPair(t *testing.T, db *gorm.DB, identityID, email string) string {
	t.Helper()
	memberID := uuid.NewString()
	name := "Email fixture " + memberID
	now := time.Now().UTC()
	require.NoError(t, db.Exec(
		`INSERT INTO account_identity (id) VALUES (?::uuid) ON CONFLICT (id) DO NOTHING`, identityID,
	).Error)
	require.NoError(t, db.Exec(
		"UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid",
		memberID,
		identityID,
	).Error)
	require.NoError(t, db.Create(&model.Member{
		ID: memberID, AccountIdentityID: &identityID, Nickname: name, Onboarded: true,
		PrimaryEmail: &email, AvailableEmails: []string{email}, SocialLinks: map[string]string{},
		CreatedAt: now, UpdatedAt: now,
	}).Error)
	return memberID
}
