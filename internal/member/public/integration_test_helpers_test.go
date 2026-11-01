//go:build integration

package public

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// newPublicIntegrationDB starts the test's own backend stack: its own database
// and SpiceDB datastore, closed when the test ends.
func newPublicIntegrationDB(t *testing.T) (*gorm.DB, *auth.SpiceDBClient) {
	t.Helper()
	stack, err := testutil.StartBackendIntegrationStack(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stack.Close()) })
	return stack.Postgres.DB, stack.SpiceDBClient
}

func seedPublicMemberIdentityLink(t *testing.T, db *gorm.DB, spiceDB *auth.SpiceDBClient, identityID, nickname string) string {
	t.Helper()
	memberID := uuid.NewString()
	require.NotEqual(t, identityID, memberID)
	var primaryEmail string
	require.NoError(t, db.Raw("SELECT traits->>'email' FROM kratos.identities WHERE id = ?::uuid", identityID).Scan(&primaryEmail).Error)
	require.NotEmpty(t, primaryEmail)
	require.NoError(t, db.Exec("INSERT INTO account_identity (id) VALUES (?::uuid) ON CONFLICT (id) DO NOTHING", identityID).Error)
	now := time.Now().UTC()
	require.NoError(t, db.Create(&model.Member{
		ID: memberID, AccountIdentityID: &identityID, Nickname: nickname, Onboarded: true,
		PrimaryEmail: &primaryEmail, AvailableEmails: pq.StringArray{primaryEmail},
		SocialLinks: map[string]string{}, CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, db.Exec("UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid", memberID, identityID).Error)
	publicSyncIntegrationRole(t, spiceDB, identityID, policyv1.Role.User())
	return memberID
}

func seedPublicPost(t *testing.T, db *gorm.DB, postID, slug string, publishedAt time.Time) {
	t.Helper()
	documentID := uuid.NewString()
	require.NoError(t, db.Exec(`
		INSERT INTO content_document (id, profile)
		VALUES (?::uuid, 'post')
	`, documentID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO post (id, status, slug, published_at, content_document_id)
		VALUES (?::uuid, 'POST_STATUS_PUBLISHED', ?, ?, ?::uuid)
	`, postID, slug, publishedAt, documentID).Error)
}

func publicSyncIntegrationRole(t *testing.T, spiceDB *auth.SpiceDBClient, identityID string, role policyv1.RoleID) {
	t.Helper()
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	_, err = spiceDB.SyncAccountIdentityGlobalRole(context.Background(), subject, role)
	require.NoError(t, err)
}

// newTestMemberHandler is the public Member Connect adapter of
// newTestService(t, deps).
func newTestMemberHandler(t *testing.T, deps Deps) *MemberConnectHandler {
	t.Helper()
	return NewMemberConnectHandler(newTestService(t, deps))
}
