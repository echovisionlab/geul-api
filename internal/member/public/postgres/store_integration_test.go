//go:build integration

package memberpublicpostgres_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/member/membertest"
	"github.com/echovisionlab/geul-api/internal/member/public"
	memberpublicpostgres "github.com/echovisionlab/geul-api/internal/member/public/postgres"
	"github.com/echovisionlab/geul-api/internal/testutil"
)

type statementLogger struct {
	gormlogger.Interface
	mu         sync.Mutex
	statements []string
}

func (l *statementLogger) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.statements = append(l.statements, sql)
}

func (l *statementLogger) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.statements...)
}

func publicDB(t *testing.T) *gorm.DB {
	t.Helper()
	return testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
}

// seedMember stores an onboarded Member whose Identity link is the anchor
// alone; the public reads never join Kratos.
func seedMember(t *testing.T, db *gorm.DB, nickname string, onboarded bool) (string, string) {
	t.Helper()
	memberID, identityID := uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO account_identity (id) VALUES (?::uuid)`, identityID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO member (id, account_identity_id, nickname, onboarded, bio, social_links)
		VALUES (?::uuid, ?::uuid, ?, ?, ?, '{}'::jsonb)
	`, memberID, identityID, nickname, onboarded, "Bio "+nickname).Error)
	return memberID, identityID
}

func seedPost(t *testing.T, db *gorm.DB, memberID string, status string) {
	t.Helper()
	documentID, postID := uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile) VALUES (?::uuid, 'post')`, documentID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO post (id, status, slug, published_at, content_document_id)
		VALUES (?::uuid, ?, ?, ?, ?::uuid)
	`, postID, status, "post-"+postID, time.Now().UTC(), documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO post_author (post_id, member_id, created_at) VALUES (?::uuid, ?::uuid, ?)`, postID, memberID, time.Now().UTC()).Error)
}

func publicService(t *testing.T, db *gorm.DB, authorIdentities []string) *public.Service {
	t.Helper()
	units, err := memberpublicpostgres.NewUnits(db, "https://cdn.example.test")
	require.NoError(t, err)
	spiceDB := membertest.NewAuthorizer(authztest.AllowAll())
	spiceDB.Subjects[membertest.Authors()] = authorIdentities
	service, err := public.NewService(public.Deps{Units: units, SpiceDB: spiceDB, Fence: &identitystatetest.Fence{}, Audit: &audittest.Recorder{}, TokenSecret: "public-member-store-test-secret-at-least-32-bytes"})
	require.NoError(t, err)
	return service
}

func TestListAuthorsHasAConstantStatementBudgetIntegration(t *testing.T) {
	t.Parallel()
	db := publicDB(t)
	var identities []string
	for index := range 6 {
		memberID, identityID := seedMember(t, db, "Author "+string(rune('A'+index)), true)
		identities = append(identities, identityID)
		seedPost(t, db, memberID, "POST_STATUS_PUBLISHED")
	}

	for _, limit := range []int32{1, 6} {
		budget := &statementLogger{Interface: gormlogger.Discard}
		service := publicService(t, db.Session(&gorm.Session{Logger: budget}), identities)

		authors, err := service.ListAuthors(t.Context(), public.ListAuthorsQuery{Limit: limit})
		require.NoError(t, err)
		require.Len(t, authors, int(limit))
		statements := budget.snapshot()
		require.Len(t, statements, 3, "count + batched Member + batched avatar queries")
		require.Contains(t, strings.ToLower(strings.Join(statements, "\n")), "account_identity_id")
		require.NotNil(t, authors[0].Bio)
	}
}

func TestSelectedAuthorsCountOnlyPublishedOrArchivedPostsIntegration(t *testing.T) {
	t.Parallel()
	db := publicDB(t)
	authorID, authorIdentity := seedMember(t, db, "Selected Author", true)
	idleID, idleIdentity := seedMember(t, db, "Idle Author", true)
	pendingID, pendingIdentity := seedMember(t, db, "Pending Author", false)
	seedPost(t, db, authorID, "POST_STATUS_PUBLISHED")
	seedPost(t, db, authorID, "POST_STATUS_ARCHIVED")
	seedPost(t, db, authorID, "POST_STATUS_DRAFT")

	authors, err := publicService(t, db, []string{authorIdentity, idleIdentity, pendingIdentity}).ListAuthors(
		t.Context(), public.ListAuthorsQuery{MemberIDs: []string{idleID, pendingID, authorID}},
	)
	require.NoError(t, err)
	require.Len(t, authors, 2)
	require.Equal(t, idleID, authors[0].Member.ID)
	require.Zero(t, authors[0].PostCount)
	require.Equal(t, authorID, authors[1].Member.ID)
	require.Equal(t, int32(2), authors[1].PostCount)
}

func TestUnonboardedMemberIsAbsentFromThePublicProfileAndSummariesIntegration(t *testing.T) {
	t.Parallel()
	db := publicDB(t)
	memberID, _ := seedMember(t, db, uuid.NewString(), false)

	profile, err := publicService(t, db, nil).GetPublicMember(t.Context(), public.GetPublicMemberQuery{MemberID: memberID})
	require.NoError(t, err)
	require.Nil(t, profile)
	summaries, err := memberpublicpostgres.LoadPublicMemberSummaries(t.Context(), db, "https://cdn.example.test", []string{memberID})
	require.NoError(t, err)
	require.NotContains(t, summaries, memberID)
}

func TestNewsletterMemberIDIgnoresDeletedMembersIntegration(t *testing.T) {
	t.Parallel()
	db := publicDB(t)
	memberID, identityID := seedMember(t, db, "subscriber", true)
	units, err := memberpublicpostgres.NewUnits(db, "")
	require.NoError(t, err)
	read := func() (string, error) {
		var found string
		err := units.Read(t.Context(), func(store public.Store) error {
			var err error
			found, err = store.NewsletterMemberID(t.Context(), identityID)
			return err
		})
		return found, err
	}
	found, err := read()
	require.NoError(t, err)
	require.Equal(t, memberID, found)
	require.NoError(t, db.Exec(`UPDATE member SET account_identity_id = NULL, deleted_at = now() WHERE id = ?::uuid`, memberID).Error)
	_, err = read()
	require.Error(t, err)
}
