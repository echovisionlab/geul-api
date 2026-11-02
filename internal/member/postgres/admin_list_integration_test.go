//go:build integration

package memberpostgres_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/echovisionlab/geul-api/internal/member"
	memberpostgres "github.com/echovisionlab/geul-api/internal/member/postgres"

	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

type queryCounter struct {
	logger.Interface
	count *atomic.Int64
}

func (l queryCounter) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	l.count.Add(1)
	l.Interface.Trace(ctx, begin, fc, err)
}

// seedListed stores a linked Member created at createdAt, subscribed or
// banned as asked.
func seedListed(t *testing.T, db *gorm.DB, name string, subscribed, banned bool, createdAt time.Time) (string, string) {
	t.Helper()
	memberID, identityID := uuid.NewString(), uuid.NewString()
	// Stored emails are normalized (chk_member_available_emails).
	email := strings.ToLower(name + "-" + memberID + "@example.test")
	orytest.SeedKratosIdentityFixture(t, db, orytest.KratosIdentityFixture{ID: identityID, Email: email, Name: name, Banned: banned, CreatedAt: createdAt})
	require.NoError(t, db.Exec("UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid", memberID, identityID).Error)
	require.NoError(t, db.Exec("INSERT INTO account_identity (id) VALUES (?::uuid)", identityID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO member (id, account_identity_id, nickname, onboarded, primary_email, available_emails, social_links, created_at, updated_at)
		VALUES (?::uuid, ?::uuid, ?, TRUE, ?, ARRAY[?::text], '{}'::jsonb, ?, ?)
	`, memberID, identityID, name, email, email, createdAt, createdAt).Error)
	if subscribed {
		require.NoError(t, db.Exec("INSERT INTO newsletter_subscription (identity_id, subscribed_at) VALUES (?::uuid, ?)", identityID, createdAt).Error)
	}
	return memberID, identityID
}

func pageIDs(t *testing.T, db *gorm.DB, filters []*commonv1.FilterSpec, sorts []*commonv1.SortSpec) []string {
	t.Helper()
	members, total, err := memberpostgres.NewStore(db).AdminMemberPage(t.Context(), filters, sorts, 50, 0)
	require.NoError(t, err)
	require.EqualValues(t, len(members), total)
	ids := make([]string, len(members))
	for i, value := range members {
		ids[i] = value.ID
	}
	return ids
}

func TestAdminMemberPageFiltersSortsAndKeepsAConstantQueryCountIntegration(t *testing.T) {
	t.Parallel()
	db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{BootstrapKratosStub: true, ApplyAppSchemaSQL: true}).DB
	now := time.Now().UTC().Truncate(time.Second)
	alice, _ := seedListed(t, db, "Alice", true, false, now.Add(-4*time.Hour))
	bob, _ := seedListed(t, db, "Bob", false, false, now.Add(-3*time.Hour))
	carol, _ := seedListed(t, db, "Carol", false, true, now.Add(-2*time.Hour))
	pending, pendingIdentity := seedListed(t, db, "Pending", false, false, now.Add(-90*time.Minute))
	deleted := uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO member (id, nickname, onboarded, social_links, deleted_at, created_at, updated_at) VALUES (?::uuid, 'Deleted member', TRUE, '{}'::jsonb, ?, ?, ?)`, deleted, now, now.Add(-time.Hour), now).Error)
	scheduledAt := now.Add(-time.Minute)
	require.NoError(t, db.Exec(`
		INSERT INTO user_deletion_request (id, member_id, identity_id, token, token_expires_at, confirmed_at, scheduled_at, lifecycle_state, notification_email, created_at, updated_at)
		VALUES (?::uuid, ?::uuid, ?::uuid, ?, ?, ?, ?, 'scheduled', 'pending@example.test', ?, ?)
	`, uuid.NewString(), pending, pendingIdentity, uuid.NewString(), now.Add(time.Hour), scheduledAt, scheduledAt, scheduledAt, scheduledAt).Error)

	eq := func(field, value string) []*commonv1.FilterSpec {
		return []*commonv1.FilterSpec{{Field: field, Op: commonv1.FilterOp_FILTER_OP_EQ, Value: value}}
	}
	require.Equal(t, []string{alice}, pageIDs(t, db, []*commonv1.FilterSpec{
		{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "alice"},
		{Field: "newsletter_subscribed", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "true"},
	}, []*commonv1.SortSpec{{Field: "nickname", Order: commonv1.SortOrder_SORT_ORDER_ASC}}))
	require.Equal(t, []string{carol}, pageIDs(t, db, eq("status", string(member.AdminStatusBanned)), nil))
	require.Equal(t, []string{pending}, pageIDs(t, db, eq("status", string(member.AdminStatusPendingDeletion)), nil))
	require.Equal(t, []string{deleted}, pageIDs(t, db, eq("status", string(member.AdminStatusDeleted)), nil))
	require.Equal(t, []string{deleted, pending, carol, bob, alice}, pageIDs(t, db, nil, nil))

	var queries atomic.Int64
	counted := db.Session(&gorm.Session{Logger: queryCounter{Interface: db.Config.Logger, count: &queries}})
	listAndCount := func(limit int) int64 {
		queries.Store(0)
		store := memberpostgres.NewStore(counted)
		members, _, err := store.AdminMemberPage(t.Context(), nil, []*commonv1.SortSpec{{Field: "newsletter_subscribed", Order: commonv1.SortOrder_SORT_ORDER_DESC}}, limit, 0)
		require.NoError(t, err)
		ids := make([]string, len(members))
		for i, value := range members {
			ids[i] = value.ID
		}
		_, err = store.Avatars(t.Context(), ids)
		require.NoError(t, err)
		_, err = store.TagIDs(t.Context(), ids)
		require.NoError(t, err)
		_, err = store.NewsletterSubscriptions(t.Context(), ids)
		require.NoError(t, err)
		return queries.Load()
	}
	require.Equal(t, listAndCount(1), listAndCount(10))
	require.Equal(t, int64(5), listAndCount(10))
}

func TestTagPageAppliesFiltersAndSortsIntegration(t *testing.T) {
	t.Parallel()
	db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{BootstrapKratosStub: true, ApplyAppSchemaSQL: true}).DB
	now := time.Now().UTC().Truncate(time.Second)
	first, _ := seedLinked(t, db, "first-"+uuid.NewString()[:8], true)
	second, _ := seedLinked(t, db, "second-"+uuid.NewString()[:8], true)
	alpha, editorial := uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO user_tag (id, name, created_at) VALUES (?::uuid, 'Alpha', ?), (?::uuid, 'Editorial', ?)`, alpha, now.Add(-time.Hour), editorial, now).Error)
	require.NoError(t, db.Exec(`INSERT INTO user_tag_mapping (member_id, tag_id) VALUES (?::uuid, ?::uuid), (?::uuid, ?::uuid), (?::uuid, ?::uuid)`, first, alpha, second, alpha, first, editorial).Error)
	store := memberpostgres.NewStore(db)

	filtered, total, err := store.TagPage(t.Context(), []*commonv1.FilterSpec{{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "editor"}}, nil, 50, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Equal(t, "Editorial", filtered[0].Name)

	sorted, _, err := store.TagPage(t.Context(), nil, []*commonv1.SortSpec{{Field: "user_count", Order: commonv1.SortOrder_SORT_ORDER_DESC}}, 50, 0)
	require.NoError(t, err)
	require.Len(t, sorted, 2)
	require.Equal(t, member.Tag{ID: alpha, Name: "Alpha", CreatedAt: sorted[0].CreatedAt, MemberCount: 2}, sorted[0])
	require.True(t, now.Add(-time.Hour).Equal(sorted[0].CreatedAt))
}
