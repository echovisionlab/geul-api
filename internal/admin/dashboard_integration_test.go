//go:build integration

package admin

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestAdminDashboardStatsIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	testutil.ConstrainKratosIdentityAggregateFixture(t, db)
	now := time.Now().UTC()
	adminID := uuid.NewString()
	activeUserID := uuid.NewString()
	bannedUserID := uuid.NewString()
	inactiveUserID := uuid.NewString()

	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID:        adminID,
		Email:     "dashboard-admin-" + adminID + "@example.test",
		CreatedAt: now,
	})
	seedMemberForExternalKratosIdentity(t, db, adminID, "dashboard-admin-"+adminID+"@example.test", "Dashboard admin")
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID:        activeUserID,
		Email:     "dashboard-active-" + activeUserID + "@example.test",
		CreatedAt: now,
	})
	seedMemberForExternalKratosIdentity(t, db, activeUserID, "dashboard-active-"+activeUserID+"@example.test", "Dashboard active member")
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID:        bannedUserID,
		Email:     "dashboard-banned-" + bannedUserID + "@example.test",
		Banned:    true,
		CreatedAt: now,
	})
	seedMemberForExternalKratosIdentity(t, db, bannedUserID, "dashboard-banned-"+bannedUserID+"@example.test", "Dashboard banned member")
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID:        inactiveUserID,
		Email:     "dashboard-inactive-" + inactiveUserID + "@example.test",
		State:     auth.KratosStateInactive,
		CreatedAt: now,
	})
	seedMemberForExternalKratosIdentity(t, db, inactiveUserID, "dashboard-inactive-"+inactiveUserID+"@example.test", "Dashboard inactive member")

	postID := uuid.NewString()
	draftPostDocumentID := seedServiceIntegrationContentDocument(t, db, postContentDocumentProfile)
	require.NoError(t, db.Create(&model.Post{
		ID:                postID,
		ContentDocumentID: &draftPostDocumentID,
		Slug:              stringPtr("dashboard-post-" + uuid.NewString()),
		DocumentLayout:    model.DefaultDocumentLayout(),
		Status:            model.PostStatus("POST_STATUS_DRAFT"),
		CreatedAt:         now,
		UpdatedAt:         now,
	}).Error)
	publishedPostDocumentID := seedServiceIntegrationContentDocument(t, db, postContentDocumentProfile)
	require.NoError(t, db.Create(&model.Post{
		ID:                uuid.NewString(),
		ContentDocumentID: &publishedPostDocumentID,
		Slug:              stringPtr("dashboard-post-" + uuid.NewString()),
		DocumentLayout:    model.DefaultDocumentLayout(),
		Status:            model.PostStatus("POST_STATUS_PUBLISHED"),
		CreatedAt:         now,
		UpdatedAt:         now,
	}).Error)
	pageDocumentID := seedServiceIntegrationContentDocument(t, db, pageContentDocumentProfile)
	require.NoError(t, db.Create(&model.Page{
		ID:                uuid.NewString(),
		ContentDocumentID: &pageDocumentID,
		Slug:              stringPtr("dashboard-page-" + uuid.NewString()),
		DocumentLayout:    model.DefaultDocumentLayout(),
		Status:            model.PageStatus("PAGE_STATUS_DRAFT"),
		CreatedAt:         now,
		UpdatedAt:         now,
	}).Error)
	require.NoError(t, db.Create(&model.Comment{
		ID:        uuid.NewString(),
		PostID:    postID,
		Content:   "visible dashboard comment",
		IsDeleted: false,
		CreatedAt: now,
		UpdatedAt: now,
	}).Error)
	require.NoError(t, db.Create(&model.Comment{
		ID:        uuid.NewString(),
		PostID:    postID,
		Content:   "deleted dashboard comment",
		IsDeleted: true,
		CreatedAt: now,
		UpdatedAt: now,
	}).Error)
	draftCampaignDocumentID := seedServiceIntegrationContentDocument(t, db, emailContentProfile)
	require.NoError(t, db.Create(&model.Campaign{
		ID:                uuid.NewString(),
		ContentDocumentID: &draftCampaignDocumentID,
		Name:              "Dashboard Campaign A",
		Subject:           "Dashboard A",
		Status:            "CAMPAIGN_STATUS_DRAFT",
		TargetMode:        model.CampaignTargetModeAll,
		CreatedAt:         now,
		UpdatedAt:         now,
	}).Error)
	sentCampaignDocumentID := seedServiceIntegrationContentDocument(t, db, emailContentProfile)
	require.NoError(t, db.Create(&model.Campaign{
		ID:                uuid.NewString(),
		ContentDocumentID: &sentCampaignDocumentID,
		Name:              "Dashboard Campaign B",
		Subject:           "Dashboard B",
		Status:            "CAMPAIGN_STATUS_SENT",
		TargetMode:        model.CampaignTargetModeAll,
		CreatedAt:         now,
		UpdatedAt:         now,
	}).Error)

	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, adminID, policyv1.Role.Admin())
	svc := &Service{db: db, spiceDB: spiceDB}
	ctx := adminUserIntegrationAdminCtx(adminID)

	statsResp, err := svc.GetDashboardStats(ctx, connect.NewRequest(&managev1.GetDashboardStatsRequest{}))
	require.NoError(t, err)
	stats := statsResp.Msg.GetStats()
	require.Equal(t, int32(2), stats.GetTotalMembers(), "active non-banned members include the admin and active member only")
	require.Equal(t, int32(2), stats.GetTotalPosts())
	require.Equal(t, int32(1), stats.GetTotalPages())
	require.Equal(t, int32(1), stats.GetTotalComments(), "soft-deleted comments are excluded")
	require.Equal(t, int32(2), stats.GetTotalCampaigns())

	require.Equal(t, int32(2), stats.GetTotalMembers())
}
