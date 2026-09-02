//go:build integration

package member

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
)

func TestRegistrationEmailReuseHoldChecksEveryDeletedMemberCandidate(t *testing.T) {
	db := newServiceIntegrationDB(t)
	now := time.Now().UTC()
	insertDeletedMemberEmailFixture(t, db, now.Add(-6*24*time.Hour), []string{
		"primary@example.test", "secondary@example.test",
	})

	blocked, err := RegistrationEmailReuseBlocked(t.Context(), db, "SECONDARY@example.test")
	require.NoError(t, err)
	require.True(t, blocked)

	insertDeletedMemberEmailFixture(t, db, now.Add(-8*24*time.Hour), []string{
		"expired@example.test",
	})
	blocked, err = RegistrationEmailReuseBlocked(t.Context(), db, "expired@example.test")
	require.NoError(t, err)
	require.False(t, blocked)
}

func TestPublicEmailCodeRegistrationBlocksCurrentProviderOnlyCandidate(t *testing.T) {
	db := newServiceIntegrationDB(t)
	identityID := uuid.NewString()
	email := "provider-only-" + identityID + "@example.test"
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID: identityID, Email: email,
	})
	seedActiveMemberEmailPair(t, db, identityID, email)

	blocked, err := PublicEmailCodeRegistrationBlocked(t.Context(), db, email)
	require.NoError(t, err)
	require.True(t, blocked)

	// The SSO/provisioning hold remains provider-string agnostic; provider
	// subjects may independently assert the same address.
	blocked, err = RegistrationEmailReuseBlocked(t.Context(), db, email)
	require.NoError(t, err)
	require.False(t, blocked)
}

func TestScrubExpiredMemberEmailProjectionsUsesDeletedAtRetention(t *testing.T) {
	db := newServiceIntegrationDB(t)
	now := time.Now().UTC()
	expiredID := insertDeletedMemberEmailFixture(t, db, now.AddDate(-2, 0, -1), []string{"expired@example.test"})
	retainedID := insertDeletedMemberEmailFixture(t, db, now.AddDate(-2, 0, 1), []string{"retained@example.test"})

	count, err := ScrubExpiredMemberEmailProjections(t.Context(), db, now)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)

	var expired, retained model.Member
	require.NoError(t, db.Where("id = ?::uuid", expiredID).Take(&expired).Error)
	require.NoError(t, db.Where("id = ?::uuid", retainedID).Take(&retained).Error)
	require.Nil(t, expired.PrimaryEmail)
	require.Empty(t, expired.AvailableEmails)
	require.NotNil(t, retained.PrimaryEmail)
	require.Equal(t, []string{"retained@example.test"}, []string(retained.AvailableEmails))
}

func insertDeletedMemberEmailFixture(t *testing.T, db *gorm.DB, deletedAt time.Time, emails []string) string {
	t.Helper()
	id := uuid.NewString()
	primary := emails[0]
	nickname := "Deleted " + id
	require.NoError(t, db.Exec(`
		INSERT INTO member (
			id, nickname, onboarded, primary_email, available_emails, deleted_at, created_at, updated_at
		) VALUES (?::uuid, ?, TRUE, ?, string_to_array(?, ','), ?, ?, ?)
	`, id, nickname, primary, strings.Join(emails, ","), deletedAt, deletedAt.Add(-24*time.Hour), deletedAt).Error)
	return id
}
