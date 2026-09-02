//go:build integration

package admin

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestMain(m *testing.M) {
	flag.Parse()
	suite, err := testutil.StartOryIntegrationSuite(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "start Admin integration suite: %v\n", err)
		os.Exit(1)
	}
	testutil.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	if err := testutil.RunIntegrationSuiteCleanups(); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "cleanup Admin integration runtime: %v\n", err)
		code = 1
	}
	testutil.DeactivateOryIntegrationSuite(suite)
	if err := suite.Close(); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "close Admin integration suite: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

const (
	postContentDocumentProfile = "post"
	pageContentDocumentProfile = "page"
	emailContentProfile        = "email"
)

func newServiceIntegrationDB(t *testing.T) *gorm.DB { return testutil.NewIntegrationDB(t) }

func seedMemberForExternalKratosIdentity(t *testing.T, db *gorm.DB, identityID, email, name string) string {
	t.Helper()
	memberID := testutil.PostIntegrationMemberID(identityID)
	now := time.Now().UTC()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid", memberID, identityID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO account_identity (id, created_at)
			SELECT id, created_at FROM kratos.identities WHERE id = ?::uuid
			ON CONFLICT (id) DO NOTHING`, identityID).Error; err != nil {
			return err
		}
		return tx.Create(&model.Member{
			ID: memberID, AccountIdentityID: &identityID, Nickname: name, Onboarded: true,
			PrimaryEmail: &email, AvailableEmails: []string{email}, SocialLinks: map[string]string{},
			CreatedAt: now, UpdatedAt: now,
		}).Error
	}))
	return memberID
}

func seedServiceIntegrationContentDocument(t *testing.T, db *gorm.DB, profile string) string {
	t.Helper()
	id := uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile) VALUES (?::uuid, ?)`, id, profile).Error)
	return id
}

func integrationSpiceDB(t *testing.T) *auth.SpiceDBClient { return testutil.IntegrationSpiceDB(t) }

func grantIntegrationGlobalRole(t *testing.T, spiceDB *auth.SpiceDBClient, identityID string, role policyv1.RoleID) {
	testutil.GrantIntegrationGlobalRole(t, spiceDB, identityID, role)
}

func adminUserIntegrationAdminCtx(identityID string) context.Context {
	return auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(testutil.PostIntegrationMemberID(identityID)),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true,
	})
}

func stringPtr(value string) *string { return &value }
