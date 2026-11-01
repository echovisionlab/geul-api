//go:build integration

package public_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/member/public"
	memberpublicpostgres "github.com/echovisionlab/geul-api/internal/member/public/postgres"
	"github.com/echovisionlab/geul-api/internal/testutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// The extended profile and the author bio follow the Identity's current
// role through SpiceDB's role hierarchy: an administrator is an author too.
func TestExtendedMemberProfileVisibilityFollowsCurrentIdentityRoleIntegration(t *testing.T) {
	t.Parallel()
	stack, err := testutil.StartBackendIntegrationStack(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stack.Close()) })
	db, spiceDB := stack.Postgres.DB, stack.SpiceDBClient
	now := time.Now().UTC()
	identityID, memberID := uuid.NewString(), uuid.NewString()
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{ID: identityID, Name: "Role Profile", CreatedAt: now})
	require.NoError(t, db.Exec(`INSERT INTO account_identity (id) VALUES (?::uuid)`, identityID).Error)
	require.NoError(t, db.Exec(`UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid`, memberID, identityID).Error)
	bio, website := "stored biography", "https://profile.example.test"
	require.NoError(t, db.Exec(`
		INSERT INTO member (id, account_identity_id, nickname, onboarded, bio, website, social_links)
		VALUES (?::uuid, ?::uuid, 'Role Profile', TRUE, ?, ?, jsonb_build_object('homepage', ?::text))
	`, memberID, identityID, bio, website, website).Error)
	documentID, postID := uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile) VALUES (?::uuid, 'post')`, documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO post (id, status, slug, published_at, content_document_id) VALUES (?::uuid, 'POST_STATUS_PUBLISHED', ?, ?, ?::uuid)`, postID, "role-profile-"+memberID, now, documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO post_author (post_id, member_id, created_at) VALUES (?::uuid, ?::uuid, ?)`, postID, memberID, now).Error)
	units, err := memberpublicpostgres.NewUnits(db, "https://cdn.example.test")
	require.NoError(t, err)
	service, err := public.NewService(public.Deps{Units: units, SpiceDB: spiceDB, Fence: identitystate.PostgresFence{}, Audit: &audittest.Recorder{}, TokenSecret: tokenSecret})
	require.NoError(t, err)
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)

	for _, role := range []policyv1.RoleID{policyv1.Role.User(), policyv1.Role.Author(), policyv1.Role.User(), policyv1.Role.Admin()} {
		_, err := spiceDB.SyncAccountIdentityGlobalRole(t.Context(), subject, role)
		require.NoError(t, err)
		visible := role != policyv1.Role.User()
		profile, err := service.GetPublicMember(t.Context(), public.GetPublicMemberQuery{MemberID: memberID})
		require.NoError(t, err)
		authors, err := service.ListAuthors(t.Context(), public.ListAuthorsQuery{Limit: 10})
		require.NoError(t, err)
		require.Len(t, authors, 1)
		if visible {
			require.Equal(t, &bio, profile.Bio)
			require.Equal(t, &website, profile.Website)
			require.Equal(t, map[string]string{"homepage": website}, profile.SocialLinks)
			require.Equal(t, &bio, authors[0].Bio)
			continue
		}
		require.Nil(t, profile.Bio)
		require.Nil(t, profile.Website)
		require.Empty(t, profile.SocialLinks)
		require.Nil(t, authors[0].Bio)
	}
}
