//go:build integration

package public

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/testutil"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type memberQueryBudgetLogger struct {
	gormlogger.Interface
	mu         sync.Mutex
	statements []string
}

func newMemberQueryBudgetLogger() *memberQueryBudgetLogger {
	return &memberQueryBudgetLogger{Interface: gormlogger.Discard}
}

func (l *memberQueryBudgetLogger) Trace(
	ctx context.Context,
	begin time.Time,
	fc func() (string, int64),
	err error,
) {
	sql, _ := fc()
	l.mu.Lock()
	l.statements = append(l.statements, sql)
	l.mu.Unlock()
}

func (l *memberQueryBudgetLogger) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.statements...)
}

func TestPublicMemberListAuthorsHasConstantQueryBudgetIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	ctx := context.Background()
	createdAt := time.Date(2026, 8, 4, 1, 0, 0, 0, time.UTC)

	memberIDs := make([]string, 0, 6)
	for index := 0; index < 6; index++ {
		name := "Author " + string(rune('A'+index))
		bio := "Bio " + name
		identityID := uuid.NewString()
		testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
			ID: identityID, Name: name, CreatedAt: createdAt.Add(time.Duration(index) * time.Minute),
		})
		memberID := seedPublicMemberIdentityLink(t, db, identityID, name)
		publicSyncIntegrationRole(t, identityID, policyv1.Role.Author())
		memberIDs = append(memberIDs, memberID)
		require.NoError(t, db.Model(&model.Member{}).Where("id = ?", memberID).Updates(structured.Fields{
			"bio":        bio,
			"created_at": createdAt.Add(time.Duration(index) * time.Minute),
			"updated_at": createdAt.Add(time.Duration(index) * time.Minute),
		}).Error)

		postID := uuid.NewString()
		seedPublicPost(t, db, postID, "member-budget-"+memberID, createdAt)
		require.NoError(t, db.Exec(`
			INSERT INTO post_author (post_id, member_id, created_at)
			VALUES (?::uuid, ?::uuid, ?)
		`, postID, memberID, createdAt).Error)
	}

	for _, limit := range []int32{1, int32(len(memberIDs))} {
		t.Run("limit_"+string(rune('0'+limit)), func(t *testing.T) {
			budget := newMemberQueryBudgetLogger()
			budgetDB := db.Session(&gorm.Session{Logger: budget})
			svc := NewMemberService(budgetDB, "https://cdn.example.test", publicIntegrationSpiceDB)

			response, err := svc.ListAuthors(
				ctx,
				connect.NewRequest(&openv1.ListAuthorsRequest{Limit: limit}),
			)
			require.NoError(t, err)
			require.Len(t, response.Msg.Authors, int(limit))

			statements := budget.snapshot()
			require.Len(t, statements, 3, "count + batched Member + batched avatar queries")
			require.Contains(t, strings.ToLower(strings.Join(statements, "\n")), "account_identity_id")
		})
	}
}

func TestPublicMemberListAuthorsReturnsOnlySelectedEffectiveAuthorsInRequestOrderIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seed := func(name string, role policyv1.RoleID) (string, string) {
		t.Helper()
		identityID := uuid.NewString()
		testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
			ID: identityID, Name: name, CreatedAt: now,
		})
		memberID := seedPublicMemberIdentityLink(t, db, identityID, name)
		publicSyncIntegrationRole(t, identityID, role)
		return memberID, identityID
	}
	authorID, _ := seed("Selected Author", policyv1.Role.Author())
	adminID, _ := seed("Selected Admin", policyv1.Role.Admin())
	userID, _ := seed("Ordinary User", policyv1.Role.User())
	postID := uuid.NewString()
	seedPublicPost(t, db, postID, "selected-author-post", now)
	require.NoError(t, db.Exec(`
		INSERT INTO post_author (post_id, member_id, created_at)
		VALUES (?::uuid, ?::uuid, ?)
	`, postID, authorID, now).Error)

	response, err := NewMemberService(db, "https://cdn.example.test", publicIntegrationSpiceDB).ListAuthors(
		ctx,
		connect.NewRequest(&openv1.ListAuthorsRequest{MemberIds: []string{adminID, userID, authorID}}),
	)
	require.NoError(t, err)
	require.Len(t, response.Msg.Authors, 2)
	require.Equal(t, adminID, response.Msg.Authors[0].Member.Id)
	require.Zero(t, response.Msg.Authors[0].PostCount)
	require.Equal(t, authorID, response.Msg.Authors[1].Member.Id)
	require.Equal(t, int32(1), response.Msg.Authors[1].PostCount)

	_, err = NewMemberService(db, "https://cdn.example.test", publicIntegrationSpiceDB).ListAuthors(
		ctx,
		connect.NewRequest(&openv1.ListAuthorsRequest{MemberIds: []string{authorID, authorID}}),
	)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestExtendedMemberProfileVisibilityFollowsCurrentIdentityRoleIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	identityID := uuid.NewString()
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID: identityID, Name: "Role Profile", CreatedAt: now,
	})
	memberID := seedPublicMemberIdentityLink(t, db, identityID, "Role Profile")
	bio := "stored biography"
	website := "https://profile.example.test"
	require.NoError(t, db.Model(&model.Member{}).Where("id = ?", memberID).Updates(structured.Fields{
		"bio":          bio,
		"website":      website,
		"social_links": map[string]string{"homepage": website},
	}).Error)
	postID := uuid.NewString()
	seedPublicPost(t, db, postID, "role-profile-"+memberID, now)
	require.NoError(t, db.Exec(`
		INSERT INTO post_author (post_id, member_id, created_at)
		VALUES (?::uuid, ?::uuid, ?)
	`, postID, memberID, now).Error)

	service := NewMemberService(db, "https://cdn.example.test", publicIntegrationSpiceDB)
	assertVisibility := func(t *testing.T, visible bool) {
		t.Helper()
		profile, err := service.GetPublicMember(ctx, connect.NewRequest(&openv1.GetPublicMemberRequest{MemberId: memberID}))
		require.NoError(t, err)
		require.NotNil(t, profile.Msg.Member)
		authors, err := service.ListAuthors(ctx, connect.NewRequest(&openv1.ListAuthorsRequest{Limit: 10}))
		require.NoError(t, err)
		require.Len(t, authors.Msg.Authors, 1)
		if visible {
			require.Equal(t, bio, profile.Msg.Member.GetBio())
			require.Equal(t, website, profile.Msg.Member.GetWebsite())
			require.Equal(t, map[string]string{"homepage": website}, profile.Msg.Member.SocialLinks)
			require.Equal(t, bio, authors.Msg.Authors[0].GetBio())
			return
		}
		require.Nil(t, profile.Msg.Member.Bio)
		require.Nil(t, profile.Msg.Member.Website)
		require.Empty(t, profile.Msg.Member.SocialLinks)
		require.Nil(t, authors.Msg.Authors[0].Bio)
	}

	assertVisibility(t, false)
	for _, role := range []policyv1.RoleID{policyv1.Role.Author(), policyv1.Role.User(), policyv1.Role.Admin()} {
		publicSyncIntegrationRole(t, identityID, role)
		assertVisibility(t, role != policyv1.Role.User())
	}

	var stored model.Member
	require.NoError(t, db.Where("id = ?", memberID).Take(&stored).Error)
	require.NotNil(t, stored.Bio)
	require.Equal(t, bio, *stored.Bio)
}

func TestDeletedMemberMarkerSurvivesPublicProfileAndAuthorProjectionIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	ctx := context.Background()
	memberID := uuid.NewString()
	postID := uuid.NewString()
	deletedAt := time.Now().UTC()
	nickname := "FormerMember"

	require.NoError(t, db.Create(&model.Member{
		ID: memberID, Nickname: nickname, Onboarded: true, SocialLinks: map[string]string{}, DeletedAt: &deletedAt,
		CreatedAt: deletedAt.Add(-time.Hour), UpdatedAt: deletedAt,
	}).Error)
	_, avatarAssetID := seedCanonicalPublicFileFixture(t, db, "deleted-member-avatar.webp", "image/webp", "avatar")
	require.NoError(t, db.Create(&model.PublicAssetBinding{
		AssetID: avatarAssetID, OwnerType: "member", OwnerID: memberID, BindingKey: "avatar",
	}).Error)
	seedPublicPost(t, db, postID, "deleted-member-"+memberID, deletedAt.Add(-time.Minute))
	require.NoError(t, db.Exec(`
		INSERT INTO post_author (post_id, member_id)
		VALUES (?::uuid, ?::uuid)
	`, postID, memberID).Error)

	svc := NewMemberService(db, "https://cdn.example.test", publicIntegrationSpiceDB)
	profile, err := svc.GetPublicMember(
		ctx,
		connect.NewRequest(&openv1.GetPublicMemberRequest{MemberId: memberID}),
	)
	require.NoError(t, err)
	require.NotNil(t, profile.Msg.Member)
	require.True(t, profile.Msg.Member.Summary.Deleted)
	require.Equal(t, nickname, profile.Msg.Member.Summary.GetNickname())
	require.Nil(t, profile.Msg.Member.Summary.AvatarAsset)
	require.Nil(t, profile.Msg.Member.Bio)
	require.Nil(t, profile.Msg.Member.Website)
	require.Empty(t, profile.Msg.Member.SocialLinks)

	authors, err := svc.ListAuthors(
		ctx,
		connect.NewRequest(&openv1.ListAuthorsRequest{Limit: 10}),
	)
	require.NoError(t, err)
	require.Len(t, authors.Msg.Authors, 1)
	require.True(t, authors.Msg.Authors[0].Member.Deleted)
	require.Equal(t, nickname, authors.Msg.Authors[0].Member.GetNickname())
	require.Nil(t, authors.Msg.Authors[0].Member.AvatarAsset)
	require.Nil(t, authors.Msg.Authors[0].Bio)
}

func TestUnlinkedMemberIsImmediatelyScrubbedAsPublicTombstoneIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	memberID := uuid.NewString()
	nickname := "UnlinkedPrivateName"
	bio := "Unlinked private bio"
	website := "https://unlinked-private.example.test"
	now := time.Now().UTC()
	require.NoError(t, db.Create(&model.Member{
		ID: memberID, Nickname: nickname, Onboarded: true, Bio: &bio, Website: &website,
		SocialLinks: map[string]string{"private": website}, CreatedAt: now, UpdatedAt: now,
	}).Error)
	_, avatarAssetID := seedCanonicalPublicFileFixture(t, db, "unlinked-private-avatar.webp", "image/webp", "avatar")
	require.NoError(t, db.Create(&model.PublicAssetBinding{
		AssetID: avatarAssetID, OwnerType: "member", OwnerID: memberID, BindingKey: "avatar",
	}).Error)

	profile, err := NewMemberService(db, "https://cdn.example.test", publicIntegrationSpiceDB).GetPublicMember(
		context.Background(),
		connect.NewRequest(&openv1.GetPublicMemberRequest{MemberId: memberID}),
	)
	require.NoError(t, err)
	require.NotNil(t, profile.Msg.Member)
	require.True(t, profile.Msg.Member.Summary.Deleted)
	require.Equal(t, nickname, profile.Msg.Member.Summary.GetNickname())
	require.Nil(t, profile.Msg.Member.Summary.AvatarAsset)
	require.Nil(t, profile.Msg.Member.Bio)
	require.Nil(t, profile.Msg.Member.Website)
	require.Empty(t, profile.Msg.Member.SocialLinks)
}

func TestUnonboardedMemberIsAbsentFromPublicProfileAndSharedProjectionIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	identityID := uuid.NewString()
	memberID := uuid.NewString()
	email := "unonboarded@example.test"
	now := time.Now().UTC()
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID: identityID, CreatedAt: now,
	})
	require.NoError(t, db.Exec(
		`INSERT INTO account_identity (id) VALUES (?::uuid)`, identityID,
	).Error)
	require.NoError(t, db.Create(&model.Member{
		ID:                memberID,
		AccountIdentityID: &identityID,
		Nickname:          memberID,
		Onboarded:         false,
		PrimaryEmail:      &email,
		AvailableEmails:   []string{email},
		SocialLinks:       map[string]string{},
		CreatedAt:         now,
		UpdatedAt:         now,
	}).Error)
	require.NoError(t, db.Exec(
		`UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid`, memberID, identityID,
	).Error)

	profile, err := NewMemberService(db, "https://cdn.example.test", publicIntegrationSpiceDB).GetPublicMember(
		context.Background(),
		connect.NewRequest(&openv1.GetPublicMemberRequest{MemberId: memberID}),
	)
	require.NoError(t, err)
	require.Nil(t, profile.Msg.Member)

	summaries, err := LoadPublicMemberSummaries(
		context.Background(), db, "https://cdn.example.test", []string{memberID},
	)
	require.NoError(t, err)
	require.NotContains(t, summaries, memberID)
}
