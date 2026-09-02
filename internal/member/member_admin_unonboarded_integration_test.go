//go:build integration

package member

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAdminProfileAvatarAndTagsKeepMemberUnonboardedIntegration(t *testing.T) {
	db := newConcurrentServiceIntegrationPostgres(t).DB
	identityID := integrationTestUUID()
	memberID := seedAdminEditableMember(t, db, identityID, "pending-"+uuid.NewString())
	var tagIDs []string
	t.Cleanup(func() {
		require.NoError(t, db.Exec(`
			DELETE FROM public.domain_audit
			WHERE actor_member_id = ?::uuid OR target_id = ?
		`, memberID, memberID).Error)
		require.NoError(t, db.Exec(`DELETE FROM user_tag_mapping WHERE member_id=?::uuid`, memberID).Error)
		for _, tagID := range tagIDs {
			require.NoError(t, db.Exec(`DELETE FROM user_tag WHERE id=?::uuid`, tagID).Error)
		}
		require.NoError(t, db.Exec(`DELETE FROM member WHERE id=?::uuid`, memberID).Error)
		require.NoError(t, db.Exec(`DELETE FROM kratos.identities WHERE id=?::uuid`, identityID).Error)
	})
	require.NoError(t, db.Exec(`UPDATE member SET onboarded=FALSE WHERE id=?::uuid`, memberID).Error)

	service := &MemberService{
		db:          db,
		cdnDomain:   "https://cdn.example.test",
		auditWriter: apitelemetry.NewDurableWriter(db),
	}
	nickname := "Edited by Admin"
	bio := "Admin-authored biography"
	website := "https://admin-edited.example.test"
	links := map[string]string{"homepage": website}
	adminCtx := memberAuditContextForPair(t, identityID, memberID)
	profile, err := service.updateMemberProfileAsAdmin(adminCtx, memberID, &nickname, &bio, &website, links)
	require.NoError(t, err)
	require.Equal(t, nickname, profile.Summary.Nickname)
	require.Equal(t, bio, profile.GetBio())
	require.Equal(t, website, profile.GetWebsite())
	require.Equal(t, links, profile.SocialLinks)
	requireMemberOnboarded(t, db, memberID, false)
	var stored model.Member
	require.NoError(t, db.Where("id=?::uuid", memberID).Take(&stored).Error)
	require.NotNil(t, stored.Bio)
	require.Equal(t, bio, *stored.Bio)
	require.NotNil(t, stored.Website)
	require.Equal(t, website, *stored.Website)
	require.Equal(t, links, stored.SocialLinks)

	firstTag := model.UserTag{ID: uuid.NewString(), Name: "first-" + uuid.NewString(), CreatedAt: time.Now().UTC()}
	secondTag := model.UserTag{ID: uuid.NewString(), Name: "second-" + uuid.NewString(), CreatedAt: time.Now().UTC()}
	tagIDs = []string{firstTag.ID, secondTag.ID}
	require.NoError(t, db.Create(&firstTag).Error)
	require.NoError(t, db.Create(&secondTag).Error)
	require.NoError(t, db.Create(&model.UserTagMapping{MemberID: memberID, TagID: firstTag.ID}).Error)
	tags, err := service.replaceMemberTags(adminCtx, memberID, []string{secondTag.ID, secondTag.ID})
	require.NoError(t, err)
	require.Equal(t, []string{secondTag.ID}, tags)
	requireMemberOnboarded(t, db, memberID, false)
	var storedTagIDs []string
	require.NoError(t, db.Model(&model.UserTagMapping{}).
		Where("member_id=?::uuid", memberID).
		Order("tag_id").
		Pluck("tag_id", &storedTagIDs).Error)
	require.Equal(t, []string{secondTag.ID}, storedTagIDs)

	_, err = service.replaceMemberTags(adminCtx, memberID, []string{uuid.NewString()})
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	storedTagIDs = nil
	require.NoError(t, db.Model(&model.UserTagMapping{}).
		Where("member_id=?::uuid", memberID).
		Order("tag_id").
		Pluck("tag_id", &storedTagIDs).Error)
	require.Equal(t, []string{secondTag.ID}, storedTagIDs)

	avatarSummary, err := service.deleteAvatar(adminCtx, memberID, true)
	require.NoError(t, err)
	require.Equal(t, memberID, avatarSummary.Id)
	requireMemberOnboarded(t, db, memberID, false)

	require.NoError(t, db.Exec(`UPDATE member SET account_identity_id=NULL WHERE id=?::uuid`, memberID).Error)
	_, err = service.replaceMemberTags(adminCtx, memberID, []string{firstTag.ID})
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	storedTagIDs = nil
	require.NoError(t, db.Model(&model.UserTagMapping{}).
		Where("member_id=?::uuid", memberID).
		Order("tag_id").
		Pluck("tag_id", &storedTagIDs).Error)
	require.Equal(t, []string{secondTag.ID}, storedTagIDs)
}

func seedAdminEditableMember(t *testing.T, db *gorm.DB, identityID, nickname string) string {
	t.Helper()
	memberID := uuid.NewString()
	email := identityID + "@example.test"
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID: identityID, Email: email, Name: nickname,
	})
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(
			`UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid`, memberID, identityID,
		).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO account_identity (id) VALUES (?::uuid)`, identityID).Error; err != nil {
			return err
		}
		return tx.Create(&model.Member{
			ID: memberID, AccountIdentityID: &identityID, Nickname: nickname, Onboarded: true,
			PrimaryEmail: &email, AvailableEmails: []string{email}, SocialLinks: map[string]string{},
		}).Error
	}))
	return memberID
}

func requireMemberOnboarded(t *testing.T, db *gorm.DB, memberID string, want bool) {
	t.Helper()
	var onboarded bool
	require.NoError(t, db.Raw(`SELECT onboarded FROM member WHERE id=?::uuid`, memberID).Scan(&onboarded).Error)
	require.Equal(t, want, onboarded)
}
