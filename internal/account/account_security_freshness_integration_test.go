//go:build integration

package account

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestAccountSecurityMutationsRejectStaleCurrentSessionIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	identityID := uuid.NewString()
	memberID := uuid.NewString()
	sessionID := uuid.NewString()
	otherSessionID := uuid.NewString()
	email := "stale-security-" + identityID + "@example.test"

	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID: identityID, Email: email,
	})
	require.NoError(t, db.Exec(
		`INSERT INTO account_identity (id, created_at)
		 SELECT id, created_at FROM kratos.identities WHERE id = ?::uuid`,
		identityID,
	).Error)
	require.NoError(t, db.Exec(
		`UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid`,
		memberID,
		identityID,
	).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO member (id, account_identity_id, nickname, onboarded, primary_email, available_emails)
		VALUES (?::uuid, ?::uuid, 'Stale security session', TRUE, ?, ARRAY[?::text])
	`, memberID, identityID, email, email).Error)
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
	`, sessionID, time.Now().UTC().Add(-3*time.Hour-time.Second), identityID).Error)

	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(identityID),
		MemberID:      auth.MemberID(memberID),
		SessionID:     auth.SessionID(sessionID),
		Authenticated: true,
	})
	service := &AccountService{db: db}

	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "set canonical email",
			call: func() error {
				_, err := service.SetMyCanonicalEmail(ctx, connect.NewRequest(&managev1.SetMyCanonicalEmailRequest{Email: email}))
				return err
			},
		},
		{
			name: "request email change",
			call: func() error {
				_, err := service.RequestEmailChange(ctx, connect.NewRequest(&managev1.RequestEmailChangeRequest{NewEmail: "new-" + email}))
				return err
			},
		},
		{
			name: "revoke session",
			call: func() error {
				_, err := service.RevokeMySession(ctx, connect.NewRequest(&managev1.RevokeMySessionRequest{SessionId: otherSessionID}))
				return err
			},
		},
		{
			name: "revoke other sessions",
			call: func() error {
				_, err := service.RevokeMyOtherSessions(ctx, connect.NewRequest(&managev1.RevokeMyOtherSessionsRequest{}))
				return err
			},
		},
		{
			name: "request account deletion",
			call: func() error {
				_, err := service.RequestAccountDeletion(ctx, connect.NewRequest(&managev1.RequestAccountDeletionRequest{}))
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.call()
			require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
			require.ErrorContains(t, err, "reauthenticate")
		})
	}
}
