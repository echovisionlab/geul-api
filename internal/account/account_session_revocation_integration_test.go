//go:build integration

package account

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRevokeOtherSessionsPreservesCurrentSessionIntegration(t *testing.T) {
	db := newConcurrentServiceIntegrationDB(t)
	identityID := uuid.NewString()
	emailAddress := "session-revoke-" + identityID + "@example.test"
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID: identityID, Email: emailAddress,
	})
	currentSessionID := uuid.NewString()
	otherSessionID := uuid.NewString()
	for _, sessionID := range []string{currentSessionID, otherSessionID} {
		require.NoError(t, db.Exec(`
			INSERT INTO kratos.sessions (
				id, identity_id, active, authenticated_at, expires_at,
				created_at, updated_at, nid, authentication_methods
			)
			SELECT
				?::uuid, id, TRUE, ?, CURRENT_TIMESTAMP + INTERVAL '1 hour',
				CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, nid, '[]'::jsonb
			FROM kratos.identities
			WHERE id = ?::uuid
		`, sessionID, time.Now().UTC(), identityID).Error)
	}

	manager := newCredentialMutationIdentityManager(identityID, emailAddress)
	revoked, err := revokeOtherSessions(t.Context(), db, manager, identityID, currentSessionID)
	require.NoError(t, err)
	require.Equal(t, []string{otherSessionID}, revoked)
	require.Equal(t, []string{"session:" + otherSessionID}, manager.deletedSessions)
}
