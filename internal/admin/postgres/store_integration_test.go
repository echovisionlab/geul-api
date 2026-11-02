//go:build integration

package adminpostgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	campaignadapter "github.com/echovisionlab/geul-api/internal/adapters/campaign"
	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	pageadapter "github.com/echovisionlab/geul-api/internal/adapters/page"
	postadapter "github.com/echovisionlab/geul-api/internal/adapters/post"
	"github.com/echovisionlab/geul-api/internal/admin"
	adminpostgres "github.com/echovisionlab/geul-api/internal/admin/postgres"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

func seedMember(t *testing.T, db *gorm.DB, identity orytest.KratosIdentityFixture) {
	t.Helper()
	orytest.SeedKratosIdentityFixture(t, db, identity)
	memberID := orytest.PostIntegrationMemberID(identity.ID)
	email := identity.ID + "@example.test"
	now := time.Now().UTC()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid", memberID, identity.ID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO account_identity (id, created_at)
			SELECT id, created_at FROM kratos.identities WHERE id = ?::uuid`, identity.ID).Error; err != nil {
			return err
		}
		return tx.Create(&model.Member{
			ID: memberID, AccountIdentityID: &identity.ID, Nickname: "Dashboard member " + identity.ID, Onboarded: true,
			PrimaryEmail: &email, AvailableEmails: []string{email}, SocialLinks: map[string]string{},
			CreatedAt: now, UpdatedAt: now,
		}).Error
	}))
}

func seedDocument(t *testing.T, db *gorm.DB, profile string) *string {
	t.Helper()
	id := uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile) VALUES (?::uuid, ?)`, id, profile).Error)
	return &id
}

func slug(prefix string) *string {
	value := prefix + "-" + uuid.NewString()
	return &value
}

// The dashboard counts each owner's rows through the owner's adapter in one
// read snapshot.
func TestDashboardCountsTheOwnersRowsIntegration(t *testing.T) {
	t.Parallel()
	db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{BootstrapKratosStub: true, ApplyAppSchemaSQL: true}).DB
	seedMember(t, db, orytest.KratosIdentityFixture{ID: uuid.NewString()})
	seedMember(t, db, orytest.KratosIdentityFixture{ID: uuid.NewString()})
	seedMember(t, db, orytest.KratosIdentityFixture{ID: uuid.NewString(), Banned: true})
	seedMember(t, db, orytest.KratosIdentityFixture{ID: uuid.NewString(), State: auth.KratosStateInactive})
	now := time.Now().UTC()
	postID := uuid.NewString()
	for _, post := range []model.Post{
		{ID: postID, Status: "POST_STATUS_DRAFT"},
		{ID: uuid.NewString(), Status: "POST_STATUS_PUBLISHED"},
	} {
		post.ContentDocumentID, post.Slug, post.DocumentLayout = seedDocument(t, db, "post"), slug("post"), model.DefaultDocumentLayout()
		post.CreatedAt, post.UpdatedAt = now, now
		require.NoError(t, db.Create(&post).Error)
	}
	require.NoError(t, db.Create(&model.Page{
		ID: uuid.NewString(), ContentDocumentID: seedDocument(t, db, "page"), Slug: slug("page"),
		DocumentLayout: model.DefaultDocumentLayout(), Status: "PAGE_STATUS_DRAFT", CreatedAt: now, UpdatedAt: now,
	}).Error)
	for _, deleted := range []bool{false, true} {
		require.NoError(t, db.Create(&model.Comment{
			ID: uuid.NewString(), PostID: postID, Content: "comment", IsDeleted: deleted, CreatedAt: now, UpdatedAt: now,
		}).Error)
	}
	for _, status := range []string{"CAMPAIGN_STATUS_DRAFT", "CAMPAIGN_STATUS_SENT"} {
		require.NoError(t, db.Create(&model.Campaign{
			ID: uuid.NewString(), ContentDocumentID: seedDocument(t, db, "email"), Name: status, Subject: status,
			Status: status, TargetMode: model.CampaignTargetModeAll, CreatedAt: now, UpdatedAt: now,
		}).Error)
	}
	units, err := adminpostgres.NewUnits(db, adminpostgres.Ports{
		Members: memberadapter.DashboardMembers{}, Posts: postadapter.DashboardPosts{},
		Pages: pageadapter.DashboardPages{}, Campaigns: campaignadapter.DashboardCampaigns{},
	})
	require.NoError(t, err)

	var got admin.DashboardStats
	require.NoError(t, units.Read(t.Context(), func(store admin.Store) error {
		ctx := context.WithoutCancel(t.Context())
		var err error
		for _, count := range []struct {
			into  *int64
			count func(context.Context) (int64, error)
		}{
			{&got.Members, store.CountActiveMembers}, {&got.Posts, store.CountPosts}, {&got.Pages, store.CountPages},
			{&got.Comments, store.CountComments}, {&got.Campaigns, store.CountCampaigns},
		} {
			if *count.into, err = count.count(ctx); err != nil {
				return err
			}
		}
		return nil
	}))

	require.Equal(t, admin.DashboardStats{Members: 2, Posts: 2, Pages: 1, Comments: 1, Campaigns: 2}, got,
		"banned and inactive Members and deleted comments are not counted")
}
