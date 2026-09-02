//go:build integration

package public

import (
	"context"
	"fmt"
	"os"
	"sync"
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

var publicIntegrationPostgres *testutil.AppPostgres
var publicIntegrationSpiceDB *auth.SpiceDBClient
var publicIntegrationStack *testutil.BackendIntegrationStack
var publicIntegrationPostgresOnce sync.Once
var publicIntegrationPostgresCleanup func() error
var publicIntegrationPostgresErr error
var publicIntegrationStateMu sync.Mutex

func TestMain(m *testing.M) {
	code := m.Run()
	if publicIntegrationPostgresCleanup != nil {
		if err := publicIntegrationPostgresCleanup(); err != nil {
			fmt.Fprintf(os.Stderr, "cleanup member public integration postgres: %v\n", err)
			if code == 0 {
				code = 1
			}
		}
	}
	os.Exit(code)
}

func sharedPublicIntegrationPostgres() (*testutil.AppPostgres, error) {
	publicIntegrationPostgresOnce.Do(func() {
		stack, err := testutil.StartBackendIntegrationStack(context.Background())
		if err != nil {
			publicIntegrationPostgresErr = err
			return
		}
		publicIntegrationPostgres = stack.Postgres
		publicIntegrationSpiceDB = stack.SpiceDBClient
		publicIntegrationStack = stack
		publicIntegrationPostgresCleanup = stack.Close
	})
	return publicIntegrationPostgres, publicIntegrationPostgresErr
}

func newPublicIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	preparePublicIntegrationState(t)
	pg, err := sharedPublicIntegrationPostgres()
	require.NoError(t, err)
	return pg.DB
}

func preparePublicIntegrationState(t *testing.T) {
	t.Helper()
	publicIntegrationStateMu.Lock()
	t.Cleanup(publicIntegrationStateMu.Unlock)

	_, err := sharedPublicIntegrationPostgres()
	require.NoError(t, err)
	reset := func() error {
		if err := testutil.ResetBackendIntegrationState(context.Background(), publicIntegrationStack); err != nil {
			return err
		}
		for _, lookup := range []policyv1.SubjectLookup{
			policyv1.Platform.LookupAdminSubjects(),
			policyv1.Platform.LookupAuthorSubjects(),
			policyv1.Platform.LookupUserSubjects(),
		} {
			subjects, err := publicIntegrationSpiceDB.LookupGlobalSubjects(context.Background(), lookup)
			if err != nil {
				return fmt.Errorf("verify SpiceDB %s baseline: %w", lookup.Permission(), err)
			}
			if len(subjects) != 0 {
				return fmt.Errorf("SpiceDB %s baseline retains %d account identities", lookup.Permission(), len(subjects))
			}
		}
		return nil
	}
	require.NoError(t, reset())
	t.Cleanup(func() { require.NoError(t, reset()) })
}

func seedPublicMemberIdentityLink(t *testing.T, db *gorm.DB, identityID, nickname string) string {
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
	publicSyncIntegrationRole(t, identityID, policyv1.Role.User())
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

func publicSyncIntegrationRole(t *testing.T, identityID string, role policyv1.RoleID) {
	t.Helper()
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	_, err = publicIntegrationSpiceDB.SyncAccountIdentityGlobalRole(context.Background(), subject, role)
	require.NoError(t, err)
}
