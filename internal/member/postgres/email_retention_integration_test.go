//go:build integration

package memberpostgres_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/member"
	memberpostgres "github.com/echovisionlab/geul-api/internal/member/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

func emailDB(t *testing.T) *gorm.DB {
	t.Helper()
	return pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{BootstrapKratosStub: true, ApplyAppSchemaSQL: true}).DB
}

func insertDeletedMemberEmailFixture(t *testing.T, db *gorm.DB, deletedAt time.Time, emails []string) string {
	t.Helper()
	id := uuid.NewString()
	require.NoError(t, db.Exec(`
		INSERT INTO member (
			id, nickname, onboarded, primary_email, available_emails, deleted_at, created_at, updated_at
		) VALUES (?::uuid, ?, TRUE, ?, string_to_array(?, ','), ?, ?, ?)
	`, id, "Deleted "+id, emails[0], strings.Join(emails, ","), deletedAt, deletedAt.Add(-24*time.Hour), deletedAt).Error)
	return id
}

func TestRegistrationEmailReuseHoldChecksEveryDeletedMemberCandidateIntegration(t *testing.T) {
	t.Parallel()
	db := emailDB(t)
	now := time.Now().UTC()
	insertDeletedMemberEmailFixture(t, db, now.Add(-6*24*time.Hour), []string{"primary@example.test", "secondary@example.test"})

	blocked, err := member.RegistrationEmailReuseBlocked(t.Context(), db, "SECONDARY@example.test")
	require.NoError(t, err)
	require.True(t, blocked)
	held, err := memberpostgres.Bind(db).EmailReuseHeld(t.Context(), "secondary@example.test")
	require.NoError(t, err)
	require.True(t, held)

	insertDeletedMemberEmailFixture(t, db, now.Add(-8*24*time.Hour), []string{"expired@example.test"})
	blocked, err = member.RegistrationEmailReuseBlocked(t.Context(), db, "expired@example.test")
	require.NoError(t, err)
	require.False(t, blocked)
}

func TestPublicEmailCodeRegistrationBlocksCurrentProviderOnlyCandidateIntegration(t *testing.T) {
	t.Parallel()
	db := emailDB(t)
	email := "provider-only-" + uuid.NewString() + "@example.test"
	memberID, _ := seedLinked(t, db, "provider-only", true)
	require.NoError(t, db.Exec(`UPDATE member SET primary_email = ?, available_emails = ARRAY[?::text] WHERE id = ?::uuid`, email, email, memberID).Error)

	blocked, err := memberpostgres.PublicEmailCodeRegistrationBlocked(t.Context(), db, email)
	require.NoError(t, err)
	require.True(t, blocked)

	// The SSO/provisioning hold remains provider-string agnostic; provider
	// subjects may independently assert the same address.
	blocked, err = member.RegistrationEmailReuseBlocked(t.Context(), db, email)
	require.NoError(t, err)
	require.False(t, blocked)
}

func TestScrubExpiredMemberEmailProjectionsUsesDeletedAtRetentionIntegration(t *testing.T) {
	t.Parallel()
	db := emailDB(t)
	now := time.Now().UTC()
	expiredID := insertDeletedMemberEmailFixture(t, db, now.AddDate(-2, 0, -1), []string{"expired@example.test"})
	retainedID := insertDeletedMemberEmailFixture(t, db, now.AddDate(-2, 0, 1), []string{"retained@example.test"})

	count, err := memberpostgres.ScrubExpiredMemberEmailProjections(t.Context(), db, now)
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

func TestMemberIDAllocationSkipsAnExistingUUIDNicknameIntegration(t *testing.T) {
	t.Parallel()
	db := emailDB(t)
	claimed := uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO member (id, nickname, onboarded, available_emails) VALUES (?::uuid, ?, TRUE, '{}'::text[])`, uuid.NewString(), claimed).Error)
	_, identityID := seedLinked(t, db, "identity-holder", true)
	store := memberpostgres.Bind(db)

	for candidate, want := range map[string]bool{claimed: true, identityID: true, uuid.NewString(): false} {
		occupied, err := store.MemberIDOccupied(t.Context(), candidate)
		require.NoError(t, err)
		require.Equal(t, want, occupied, candidate)
	}
}

func TestDeletionWitnessReportsTheBilateralLinkIntegration(t *testing.T) {
	t.Parallel()
	db := emailDB(t)
	memberID, identityID := seedLinked(t, db, "witness", true)
	store := memberpostgres.Bind(db)

	linked, exists, err := store.DeletionPairWitness(t.Context(), memberID, identityID)
	require.NoError(t, err)
	require.True(t, linked)
	require.True(t, exists)
	require.NoError(t, db.Exec("UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid", uuid.NewString(), identityID).Error)
	linked, exists, err = store.DeletionPairWitness(t.Context(), memberID, identityID)
	require.NoError(t, err)
	require.False(t, linked)
	require.True(t, exists)
	linked, exists, err = store.DeletionPairWitness(t.Context(), memberID, uuid.NewString())
	require.NoError(t, err)
	require.False(t, linked)
	require.False(t, exists)
}
