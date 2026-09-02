//go:build integration

package member

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type memberAdminQueryCounter struct {
	logger.Interface
	count *atomic.Int64
}

func (l memberAdminQueryCounter) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	l.count.Add(1)
	l.Interface.Trace(ctx, begin, fc, err)
}

func TestListMembersAdminFiltersSortsAndKeepsConstantQueryCount(t *testing.T) {
	db := newServiceIntegrationDB(t)
	spiceStack := testutil.SetupOryStack(t)
	now := time.Now().UTC().Truncate(time.Second)
	alice := seedMemberAdminListPair(t, db, spiceStack.SpiceDBClient, "Alice", "alice@example.test", policyv1.Role.Admin(), true, false, now.Add(-4*time.Hour))
	bob := seedMemberAdminListPair(t, db, spiceStack.SpiceDBClient, "Bob", "bob@example.test", policyv1.Role.User(), false, false, now.Add(-3*time.Hour))
	require.NoError(t, db.Exec(`UPDATE member SET onboarded=FALSE WHERE id=?::uuid`, bob.memberID).Error)
	carol := seedMemberAdminListPair(t, db, spiceStack.SpiceDBClient, "Carol", "carol@example.test", policyv1.Role.Author(), false, true, now.Add(-2*time.Hour))
	pending := seedMemberAdminListPair(t, db, spiceStack.SpiceDBClient, "Pending", "pending@example.test", policyv1.Role.User(), false, false, now.Add(-90*time.Minute))
	deletedMemberID := seedMemberAdminListTombstone(t, db, "Deleted member", "deleted@example.test", now.Add(-time.Hour))
	scheduledAt := now.Add(-time.Minute)
	notificationEmail := "pending@example.test"
	require.NoError(t, db.Create(&model.UserDeletionRequest{
		ID: uuid.NewString(), MemberID: pending.memberID, IdentityID: pending.identityID,
		Token: uuid.NewString(), TokenExpiresAt: now.Add(time.Hour), ConfirmedAt: &scheduledAt,
		ScheduledAt: &scheduledAt, LifecycleState: "scheduled",
		NotificationEmail: &notificationEmail, CreatedAt: scheduledAt, UpdatedAt: scheduledAt,
	}).Error)

	var queryCount atomic.Int64
	db = db.Session(&gorm.Session{Logger: memberAdminQueryCounter{
		Interface: db.Config.Logger,
		count:     &queryCount,
	}})
	service := &MemberService{
		db:                     db,
		spicedb:                spiceStack.SpiceDBClient,
		accountSummaryReader:   integrationAccountSummaryReader{},
		accountEmailProjection: integrationAccountEmailProjection{},
		identity: &fakeIdentityManager{identity: &auth.Identity{
			ID:         bob.identityID,
			ExternalID: bob.memberID,
			Traits:     map[string]interface{}{"email": "bob@example.test"},
		}},
	}
	adminIdentityID := uuid.NewString()
	syncIntegrationGlobalRole(t, spiceStack.SpiceDBClient, adminIdentityID, policyv1.Role.Admin())
	adminCtx := auth.WithUser(t.Context(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(adminIdentityID),
		MemberID:      auth.MemberID(uuid.NewString()),
		SessionID:     auth.SessionID(uuid.NewString()),
		Authenticated: true,
	})

	response, err := service.ListMembersAdmin(adminCtx, connect.NewRequest(&managev1.ListMembersAdminRequest{
		Filters: []*commonv1.FilterSpec{
			{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "alice"},
			{Field: "newsletter_subscribed", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "true"},
		},
		Sorts: []*commonv1.SortSpec{{Field: "nickname", Order: commonv1.SortOrder_SORT_ORDER_ASC}},
	}))
	require.NoError(t, err)
	require.Equal(t, int32(1), response.Msg.Pagination.Total)
	require.Len(t, response.Msg.Members, 1)
	require.Equal(t, alice.memberID, response.Msg.Members[0].Member.Summary.Id)
	require.True(t, response.Msg.Members[0].NewsletterSubscription.Subscribed)

	response, err = service.ListMembersAdmin(adminCtx, connect.NewRequest(&managev1.ListMembersAdminRequest{
		Filters: []*commonv1.FilterSpec{{Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "banned"}},
	}))
	require.NoError(t, err)
	require.Len(t, response.Msg.Members, 1)
	require.Equal(t, carol.memberID, response.Msg.Members[0].Member.Summary.Id)

	response, err = service.ListMembersAdmin(adminCtx, connect.NewRequest(&managev1.ListMembersAdminRequest{
		Filters: []*commonv1.FilterSpec{{Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "pending_deletion"}},
	}))
	require.NoError(t, err)
	require.Len(t, response.Msg.Members, 1)
	require.Equal(t, pending.memberID, response.Msg.Members[0].Member.Summary.Id)
	require.Equal(t, managev1.AccountStatus_ACCOUNT_STATUS_PENDING_DELETION, response.Msg.Members[0].Account.Status)

	response, err = service.ListMembersAdmin(adminCtx, connect.NewRequest(&managev1.ListMembersAdminRequest{
		Filters: []*commonv1.FilterSpec{{Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "deleted"}},
	}))
	require.NoError(t, err)
	require.Len(t, response.Msg.Members, 1)
	require.Equal(t, deletedMemberID, response.Msg.Members[0].Member.Summary.Id)
	require.Equal(t, managev1.AccountStatus_ACCOUNT_STATUS_DELETED, response.Msg.Members[0].Account.Status)

	response, err = service.ListMembersAdmin(adminCtx, connect.NewRequest(&managev1.ListMembersAdminRequest{
		Filters: []*commonv1.FilterSpec{{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "bob"}},
	}))
	require.NoError(t, err)
	require.Len(t, response.Msg.Members, 1)
	require.Equal(t, bob.memberID, response.Msg.Members[0].Member.Summary.Id)
	require.False(t, response.Msg.Members[0].Onboarded)
	detail, err := service.GetMember(adminCtx, connect.NewRequest(&managev1.GetMemberRequest{MemberId: bob.memberID}))
	require.NoError(t, err)
	require.False(t, detail.Msg.Onboarded)

	listAndCount := func(limit int32) int64 {
		queryCount.Store(0)
		response, err := service.ListMembersAdmin(adminCtx, connect.NewRequest(&managev1.ListMembersAdminRequest{
			Pagination: &commonv1.PaginationRequest{Limit: limit},
			Sorts:      []*commonv1.SortSpec{{Field: "newsletter_subscribed", Order: commonv1.SortOrder_SORT_ORDER_DESC}},
		}))
		require.NoError(t, err)
		require.NotEmpty(t, response.Msg.Members)
		return queryCount.Load()
	}
	singleRowQueries := listAndCount(1)
	allRowsQueries := listAndCount(10)
	require.Positive(t, singleRowQueries)
	require.Equal(t, singleRowQueries, allRowsQueries)
	require.LessOrEqual(t, allRowsQueries, int64(6))
}

func TestSearchMembersCanRestrictCandidatesToEffectiveAuthorsIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	spiceStack := testutil.SetupOryStack(t)
	now := time.Now().UTC().Truncate(time.Second)
	author := seedMemberAdminListPair(t, db, spiceStack.SpiceDBClient, "Author candidate", "author-candidate@example.test", policyv1.Role.Author(), false, false, now)
	admin := seedMemberAdminListPair(t, db, spiceStack.SpiceDBClient, "Admin candidate", "admin-candidate@example.test", policyv1.Role.Admin(), false, false, now)
	seedMemberAdminListPair(t, db, spiceStack.SpiceDBClient, "User candidate", "user-candidate@example.test", policyv1.Role.User(), false, false, now)

	viewerIdentityID := uuid.NewString()
	viewerCtx := auth.WithUser(t.Context(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(viewerIdentityID),
		MemberID:      auth.MemberID(uuid.NewString()),
		SessionID:     auth.SessionID(uuid.NewString()),
		Authenticated: true,
	})
	service := &MemberService{db: db, cdnDomain: "https://cdn.example.test", spicedb: spiceStack.SpiceDBClient}
	response, err := service.SearchMembers(viewerCtx, connect.NewRequest(&managev1.SearchMembersRequest{
		EffectiveAuthorsOnly: true,
		Limit:                10,
	}))
	require.NoError(t, err)
	require.Len(t, response.Msg.Members, 2)
	require.Equal(t, admin.memberID, response.Msg.Members[0].Id)
	require.Equal(t, author.memberID, response.Msg.Members[1].Id)
}

func TestListMemberTagsAdminAppliesFiltersAndSorts(t *testing.T) {
	db := newServiceIntegrationDB(t)
	spiceStack := testutil.SetupOryStack(t)
	now := time.Now().UTC().Truncate(time.Second)
	first := seedMemberAdminListPair(t, db, spiceStack.SpiceDBClient, "First", "first@example.test", policyv1.Role.User(), false, false, now)
	second := seedMemberAdminListPair(t, db, spiceStack.SpiceDBClient, "Second", "second@example.test", policyv1.Role.User(), false, false, now)
	tags := []model.UserTag{
		{Name: "Alpha", CreatedAt: now.Add(-time.Hour)},
		{Name: "Editorial", CreatedAt: now},
	}
	require.NoError(t, db.Create(&tags).Error)
	require.NoError(t, db.Create(&[]model.UserTagMapping{
		{MemberID: first.memberID, TagID: tags[0].ID},
		{MemberID: second.memberID, TagID: tags[0].ID},
		{MemberID: first.memberID, TagID: tags[1].ID},
	}).Error)

	service := &MemberService{db: db, spicedb: spiceStack.SpiceDBClient}
	adminIdentityID := uuid.NewString()
	syncIntegrationGlobalRole(t, spiceStack.SpiceDBClient, adminIdentityID, policyv1.Role.Admin())
	adminCtx := auth.WithUser(t.Context(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(adminIdentityID),
		MemberID:      auth.MemberID(uuid.NewString()),
		SessionID:     auth.SessionID(uuid.NewString()),
		Authenticated: true,
	})

	filtered, err := service.ListMemberTagsAdmin(adminCtx, connect.NewRequest(&managev1.ListMemberTagsAdminRequest{
		Filters: []*commonv1.FilterSpec{{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "editor"}},
	}))
	require.NoError(t, err)
	require.Equal(t, int32(1), filtered.Msg.Pagination.Total)
	require.Len(t, filtered.Msg.Tags, 1)
	require.Equal(t, "Editorial", filtered.Msg.Tags[0].Name)

	sorted, err := service.ListMemberTagsAdmin(adminCtx, connect.NewRequest(&managev1.ListMemberTagsAdminRequest{
		Sorts: []*commonv1.SortSpec{{Field: "user_count", Order: commonv1.SortOrder_SORT_ORDER_DESC}},
	}))
	require.NoError(t, err)
	require.Len(t, sorted.Msg.Tags, 2)
	require.Equal(t, "Alpha", sorted.Msg.Tags[0].Name)
	require.Equal(t, int32(2), sorted.Msg.Tags[0].MemberCount)
}

type memberAdminListPair struct {
	memberID   string
	identityID string
}

func seedMemberAdminListPair(t *testing.T, db *gorm.DB, spiceDB *auth.SpiceDBClient, name, email string, role policyv1.RoleID, subscribed, banned bool, createdAt time.Time) memberAdminListPair {
	t.Helper()
	pair := memberAdminListPair{memberID: uuid.NewString(), identityID: uuid.NewString()}
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID: pair.identityID, Email: email, Name: name, Banned: banned, CreatedAt: createdAt,
	})
	require.NoError(t, db.Exec(`
		INSERT INTO account_identity (id, created_at)
		SELECT id, created_at FROM kratos.identities WHERE id = ?::uuid
		ON CONFLICT (id) DO NOTHING
	`, pair.identityID).Error)
	require.NoError(t, db.Exec("UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid", pair.memberID, pair.identityID).Error)
	require.NoError(t, db.Create(&model.Member{
		ID: pair.memberID, AccountIdentityID: &pair.identityID, Nickname: name, Onboarded: true,
		PrimaryEmail: &email, AvailableEmails: []string{email}, SocialLinks: map[string]string{},
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}).Error)
	if subscribed {
		require.NoError(t, db.Create(&model.NewsletterSubscription{
			IdentityID: pair.identityID, SubscribedAt: createdAt,
		}).Error)
	}
	syncIntegrationGlobalRole(t, spiceDB, pair.identityID, role)
	return pair
}

func seedMemberAdminListTombstone(t *testing.T, db *gorm.DB, name, email string, createdAt time.Time) string {
	t.Helper()
	memberID := uuid.NewString()
	deletedAt := createdAt.Add(time.Minute)
	require.NoError(t, db.Create(&model.Member{
		ID: memberID, Nickname: name, Onboarded: true, PrimaryEmail: &email, AvailableEmails: []string{email},
		SocialLinks: map[string]string{}, DeletedAt: &deletedAt, CreatedAt: createdAt, UpdatedAt: deletedAt,
	}).Error)
	return memberID
}
