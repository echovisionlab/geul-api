//go:build integration

package pageaccess

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestMain(m *testing.M) {
	flag.Parse()
	suite, err := testutil.StartOryIntegrationSuite(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "start Page access integration suite: %v\n", err)
		os.Exit(1)
	}
	testutil.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	testutil.DeactivateOryIntegrationSuite(suite)
	if err := suite.Close(); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "close Page access integration suite: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

type exactPageAccessChecker struct {
	allowed map[string]bool
	calls   []string
}

func (c *exactPageAccessChecker) Can(_ context.Context, decision policyv1.AuthorizationDecision) (bool, error) {
	c.calls = append(c.calls, decision.EngineKey())
	return c.allowed[decision.EngineKey()], nil
}

func seedPageAccessPrincipal(t *testing.T, db *gorm.DB) context.Context {
	t.Helper()
	identityID, memberID := uuid.NewString(), uuid.NewString()
	email := identityID + "@example.test"
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{ID: identityID, Email: email, Name: "Page viewer"})
	require.NoError(t, db.Exec("UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid", memberID, identityID).Error)
	require.NoError(t, db.Exec("INSERT INTO account_identity (id) VALUES (?::uuid)", identityID).Error)
	now := time.Now().UTC()
	require.NoError(t, db.Create(&model.Member{ID: memberID, AccountIdentityID: &identityID, Nickname: "Page viewer", Onboarded: true, PrimaryEmail: &email, AvailableEmails: []string{email}, SocialLinks: map[string]string{}, CreatedAt: now, UpdatedAt: now}).Error)
	return auth.WithUser(t.Context(), &auth.UserInfo{IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(memberID), SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true})
}

func seedPageAccessRoot(t *testing.T, db *gorm.DB, policy *commonv1.PageAccessPolicy) string {
	t.Helper()
	documentID, pageID := uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec("INSERT INTO content_document (id, profile) VALUES (?::uuid, 'page')", documentID).Error)
	raw, err := Encode(policy)
	require.NoError(t, err)
	now := time.Now().UTC()
	require.NoError(t, db.Create(&model.Page{ID: pageID, ContentDocumentID: &documentID, SourceLocale: "en", DocumentLayout: model.DefaultDocumentLayout(), AccessPolicy: raw, Status: "PAGE_STATUS_PUBLISHED", CreatedAt: now, UpdatedAt: now}).Error)
	return pageID
}

func evaluateSavedPageAccess(t *testing.T, ctx context.Context, db *gorm.DB, checker PermissionChecker, pageID string) commonv1.PageAccessReason {
	t.Helper()
	var reason commonv1.PageAccessReason
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var page model.Page
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&page, "id = ?", pageID).Error; err != nil {
			return err
		}
		var err error
		reason, err = Evaluate(ctx, tx, checker, pageID, page.AccessPolicy)
		return err
	}))
	return reason
}

func savePageAccess(t *testing.T, db *gorm.DB, pageID string, policy *commonv1.PageAccessPolicy) {
	t.Helper()
	raw, err := Encode(policy)
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.Page{}).Where("id = ?", pageID).Update("access_policy", raw).Error)
}

func TestPageAccessCurrentConditionsIntegration(t *testing.T) {
	db := testutil.SetupOryStack(t).DB
	ctx := seedPageAccessPrincipal(t, db)
	principal := auth.GetUser(ctx)
	allowed, denied := commonv1.PageAccessReason_PAGE_ACCESS_REASON_ALLOWED, commonv1.PageAccessReason_PAGE_ACCESS_REASON_CONDITIONS_NOT_MET
	checker := &exactPageAccessChecker{allowed: map[string]bool{}}
	pageID := seedPageAccessRoot(t, db, nil)
	require.Equal(t, allowed, evaluateSavedPageAccess(t, t.Context(), db, checker, pageID))
	savePageAccess(t, db, pageID, &commonv1.PageAccessPolicy{Mode: commonv1.PageAccessMode_PAGE_ACCESS_MODE_AUTHENTICATED})
	require.Equal(t, commonv1.PageAccessReason_PAGE_ACCESS_REASON_AUTHENTICATION_REQUIRED, evaluateSavedPageAccess(t, t.Context(), db, checker, pageID))
	require.Equal(t, allowed, evaluateSavedPageAccess(t, ctx, db, checker, pageID))
	roles := &commonv1.PageAccessPolicy{Mode: commonv1.PageAccessMode_PAGE_ACCESS_MODE_CONDITIONS, AllowedRoles: []policyv1.AuthorizationRole{policyv1.AuthorizationRole_AUTHOR, policyv1.AuthorizationRole_ADMIN}}
	savePageAccess(t, db, pageID, roles)
	author, err := policyv1.Platform.IsAuthor()
	require.NoError(t, err)
	admin, err := policyv1.Platform.IsAdmin()
	require.NoError(t, err)
	manage, err := policyv1.Page.Manage(pageID)
	require.NoError(t, err)
	require.Equal(t, denied, evaluateSavedPageAccess(t, ctx, db, checker, pageID))
	for _, can := range []policyv1.Can{author, admin, manage} {
		checker.allowed[can.EngineKey()] = true
		require.Equal(t, allowed, evaluateSavedPageAccess(t, ctx, db, checker, pageID))
		checker.allowed[can.EngineKey()] = false
	}
	require.Contains(t, checker.calls, author.EngineKey())
	require.Contains(t, checker.calls, admin.EngineKey())
	tagA, tagB := uuid.NewString(), uuid.NewString()
	for _, id := range []string{tagA, tagB} {
		require.NoError(t, db.Create(&model.UserTag{ID: id, Name: "Access " + id}).Error)
	}
	require.NoError(t, db.Create(&model.UserTagMapping{MemberID: principal.MemberID.String(), TagID: tagB}).Error)
	policy := &commonv1.PageAccessPolicy{Mode: commonv1.PageAccessMode_PAGE_ACCESS_MODE_CONDITIONS, AllowedRoles: roles.AllowedRoles, UserTagIds: []string{tagA, tagB}, NewsletterSubscriber: true, Match: commonv1.PageAccessMatch_PAGE_ACCESS_MATCH_ANY}
	savePageAccess(t, db, pageID, policy)
	require.Equal(t, allowed, evaluateSavedPageAccess(t, ctx, db, checker, pageID), "ANY: one matching tag qualifies")
	policy.Match = commonv1.PageAccessMatch_PAGE_ACCESS_MATCH_ALL
	savePageAccess(t, db, pageID, policy)
	require.Equal(t, denied, evaluateSavedPageAccess(t, ctx, db, checker, pageID))
	checker.allowed[author.EngineKey()] = true
	require.Equal(t, denied, evaluateSavedPageAccess(t, ctx, db, checker, pageID), "ALL: newsletter group is still missing")
	require.NoError(t, db.Create(&model.NewsletterSubscription{IdentityID: principal.IdentityID.String(), SubscribedAt: time.Now().UTC()}).Error)
	require.Equal(t, allowed, evaluateSavedPageAccess(t, ctx, db, checker, pageID), "ALL: AUTHOR plus one tag plus subscription qualifies")
	require.NoError(t, db.Delete(&model.UserTagMapping{}, "member_id = ? AND tag_id = ?", principal.MemberID.String(), tagB).Error)
	require.Equal(t, denied, evaluateSavedPageAccess(t, ctx, db, checker, pageID), "revoked tag changes the next read")
	require.NoError(t, db.Create(&model.UserTagMapping{MemberID: principal.MemberID.String(), TagID: tagA}).Error)
	require.Equal(t, allowed, evaluateSavedPageAccess(t, ctx, db, checker, pageID))
	require.NoError(t, db.Delete(&model.NewsletterSubscription{}, "identity_id = ?", principal.IdentityID.String()).Error)
	require.Equal(t, denied, evaluateSavedPageAccess(t, ctx, db, checker, pageID), "unsubscribe changes the next read")
	policy.Match = commonv1.PageAccessMatch_PAGE_ACCESS_MATCH_ANY
	savePageAccess(t, db, pageID, policy)
	require.Equal(t, allowed, evaluateSavedPageAccess(t, ctx, db, checker, pageID), "a newly saved policy changes the next read")
	checker.allowed[manage.EngineKey()] = true
	policy.Match = commonv1.PageAccessMatch_PAGE_ACCESS_MATCH_ALL
	savePageAccess(t, db, pageID, policy)
	require.Equal(t, allowed, evaluateSavedPageAccess(t, ctx, db, checker, pageID), "manager override bypasses missing conditions")
}

func TestPageAccessFreshIdentityAndMalformedPolicyIntegration(t *testing.T) {
	db := testutil.SetupOryStack(t).DB
	ctx := seedPageAccessPrincipal(t, db)
	principal := auth.GetUser(ctx)
	pageID := seedPageAccessRoot(t, db, &commonv1.PageAccessPolicy{Mode: commonv1.PageAccessMode_PAGE_ACCESS_MODE_AUTHENTICATED})
	checker := &exactPageAccessChecker{allowed: map[string]bool{}}
	denied := commonv1.PageAccessReason_PAGE_ACCESS_REASON_AUTHENTICATION_REQUIRED
	require.NoError(t, db.Exec("UPDATE kratos.identities SET metadata_admin = jsonb_build_object('banned', true) WHERE id = ?::uuid", principal.IdentityID.String()).Error)
	require.Equal(t, denied, evaluateSavedPageAccess(t, ctx, db, checker, pageID), "banned database identity overrides the unchanged request principal")
	require.NoError(t, db.Exec("UPDATE kratos.identities SET metadata_admin = '{}'::jsonb, state = 'inactive' WHERE id = ?::uuid", principal.IdentityID.String()).Error)
	require.Equal(t, denied, evaluateSavedPageAccess(t, ctx, db, checker, pageID))
	require.NoError(t, db.Exec("UPDATE kratos.identities SET state = 'active' WHERE id = ?::uuid", principal.IdentityID.String()).Error)
	require.NoError(t, db.Exec("UPDATE member SET account_identity_id = NULL, deleted_at = now(), social_links = '{}'::jsonb WHERE id = ?::uuid", principal.MemberID.String()).Error)
	require.Equal(t, denied, evaluateSavedPageAccess(t, ctx, db, checker, pageID), "deleted member overrides cached principal")
	require.NoError(t, db.Exec("UPDATE member SET account_identity_id = ?::uuid, deleted_at = NULL WHERE id = ?::uuid", principal.IdentityID.String(), principal.MemberID.String()).Error)
	require.NoError(t, db.Exec("DELETE FROM kratos.identities WHERE id = ?::uuid", principal.IdentityID.String()).Error)
	require.Equal(t, denied, evaluateSavedPageAccess(t, ctx, db, checker, pageID), "deleted Kratos identity overrides cached principal")
	raw, err := protojson.Marshal(&commonv1.PageAccessPolicy{Mode: commonv1.PageAccessMode(999)})
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.Page{}).Where("id = ?", pageID).Update("access_policy", json.RawMessage(raw)).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var page model.Page
		require.NoError(t, tx.First(&page, "id = ?", pageID).Error)
		reason, err := Evaluate(ctx, tx, checker, pageID, page.AccessPolicy)
		require.Error(t, err)
		require.NotEqual(t, commonv1.PageAccessReason_PAGE_ACCESS_REASON_ALLOWED, reason)
		require.Equal(t, connect.CodeInternal, connect.CodeOf(Require(ctx, tx, checker, pageID, page.AccessPolicy)))
		return nil
	}))
}
