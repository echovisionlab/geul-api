//go:build integration

package memberpostgres_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/member/membertest"
	memberpostgres "github.com/echovisionlab/geul-api/internal/member/postgres"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/testutil"
)

// contractUnits binds the Member rows only; the contract reaches no welcome
// email port.
func contractUnits(db *gorm.DB) mutation.UnitOfWork[member.Store] {
	return mutation.Postgres(db, func(tx *gorm.DB) member.Store { return memberpostgres.Bind(tx) })
}

// seedLinked stores a Kratos stub Identity, its account anchor and a Member
// linked to it both ways.
func seedLinked(t *testing.T, db *gorm.DB, nickname string, onboarded bool) (string, string) {
	t.Helper()
	memberID, identityID := uuid.NewString(), uuid.NewString()
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{ID: identityID, Email: identityID + "@example.test", Name: nickname, CreatedAt: time.Now().UTC()})
	require.NoError(t, db.Exec("UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid", memberID, identityID).Error)
	require.NoError(t, db.Exec("INSERT INTO account_identity (id) VALUES (?::uuid)", identityID).Error)
	// A linked Member has an email projection (chk_member_active_profile).
	email := identityID + "@example.test"
	require.NoError(t, db.Exec(`
		INSERT INTO member (id, account_identity_id, nickname, onboarded, primary_email, available_emails, social_links)
		VALUES (?::uuid, ?::uuid, ?, ?, ?, ARRAY[?::text], '{}'::jsonb)
	`, memberID, identityID, nickname, onboarded, email, email).Error)
	return memberID, identityID
}

func TestPostgresStoreKeepsTheStoreContractIntegration(t *testing.T) {
	t.Parallel()
	membertest.RunStoreContract(t, func(t *testing.T) membertest.Fixture {
		// The Kratos stub backs the bilateral Member and Identity link.
		db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{BootstrapKratosStub: true, ApplyAppSchemaSQL: true}).DB
		return membertest.Fixture{
			Harness: mutationtest.PostgresHarness(db, contractUnits(db)),
			SeedLinked: func(t *testing.T, nickname string, onboarded bool) (string, string) {
				return seedLinked(t, db, nickname, onboarded)
			},
			SeedSegmentTag: func(t *testing.T, tagID string) {
				segmentID := uuid.NewString()
				require.NoError(t, db.Exec(`INSERT INTO audience_segment (id, name, segment_type) VALUES (?::uuid, ?, 'SEGMENT_TYPE_MEMBER_TAGS')`, segmentID, "segment-"+segmentID[:8]).Error)
				require.NoError(t, db.Exec(`INSERT INTO audience_segment_user_tag (audience_segment_id, user_tag_id) VALUES (?::uuid, ?::uuid)`, segmentID, tagID).Error)
			},
		}
	})
}
