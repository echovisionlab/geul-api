//go:build integration

package post_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/testutil/postintegration"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestPostMutationsRecheckAuthorityAfterRootLockIntegration(t *testing.T) {
	tests := []struct {
		name   string
		invoke func(context.Context, *postdomain.PostService, string) error
		assert func(*testing.T, *gorm.DB, string)
	}{
		{
			name: "update",
			invoke: func(ctx context.Context, service *postdomain.PostService, postID string) error {
				commentsEnabled := false
				_, err := service.UpdatePost(ctx, connect.NewRequest(&managev1.UpdatePostRequest{
					Id: postID, CommentsEnabled: &commentsEnabled,
				}))
				return err
			},
			assert: func(t *testing.T, db *gorm.DB, postID string) {
				var enabled bool
				require.NoError(t, db.Table("post").Select("comments_enabled").Where("id = ?", postID).Scan(&enabled).Error)
				require.True(t, enabled)
			},
		},
		{
			name: "publish",
			invoke: func(ctx context.Context, service *postdomain.PostService, postID string) error {
				_, err := service.PublishPost(ctx, connect.NewRequest(&managev1.PublishPostRequest{Id: postID}))
				return err
			},
			assert: func(t *testing.T, db *gorm.DB, postID string) {
				var status string
				require.NoError(t, db.Table("post").Select("status").Where("id = ?", postID).Scan(&status).Error)
				require.Equal(t, managev1.PostStatus_POST_STATUS_DRAFT.String(), status)
			},
		},
		{
			name: "delete",
			invoke: func(ctx context.Context, service *postdomain.PostService, postID string) error {
				_, err := service.DeletePost(ctx, connect.NewRequest(&managev1.DeletePostRequest{Id: postID}))
				return err
			},
			assert: func(t *testing.T, db *gorm.DB, postID string) {
				var count int64
				require.NoError(t, db.Table("post").Where("id = ?", postID).Count(&count).Error)
				require.Equal(t, int64(1), count)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, spiceDB, actorID, actorMemberID, postID := seedPostAuthorityRaceFixtureWithSpiceDB(t)
			service := postintegration.NewPostDomainService(
				t, db, "", spiceDB,
				testutil.NewPostIdentityManager(testutil.PostIntegrationIdentity(actorID, "en")),
				testutil.NewPostContentBlockStore(t),
			)

			lockTx := db.Begin()
			require.NoError(t, lockTx.Error)
			t.Cleanup(func() {
				if lockTx.Error == nil {
					_ = lockTx.Rollback().Error
				}
			})
			var lockedID string
			require.NoError(t, lockTx.Raw("SELECT id::text FROM post WHERE id = ?::uuid FOR UPDATE", postID).Scan(&lockedID).Error)

			result := make(chan error, 1)
			go func() {
				result <- test.invoke(postAuthorRaceContext(actorID), service, postID)
			}()
			requirePostMutationStillWaiting(t, result)
			require.NoError(t, lockTx.Exec(
				"DELETE FROM post_author WHERE post_id = ?::uuid AND member_id = ?::uuid",
				postID, actorMemberID,
			).Error)
			removePostAuthorRelation(t, spiceDB, actorID, postID)
			require.NoError(t, lockTx.Commit().Error)

			err := <-result
			require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
			test.assert(t, db, postID)
		})
	}
}

func TestExportedLockedPostAccessChecksAuthorityAfterRootLockIntegration(t *testing.T) {
	tests := []struct {
		name     string
		require  func(context.Context, *gorm.DB, *auth.SpiceDBClient, string) error
		wantCode connect.Code
	}{
		{name: "view", require: postdomain.RequireLockedView, wantCode: connect.CodeNotFound},
		{name: "source locale edit", require: postdomain.RequireLockedSourceLocaleEdit, wantCode: connect.CodePermissionDenied},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, spiceDB, actorID, _, postID := seedPostAuthorityRaceFixtureWithSpiceDB(t)
			rootTx := db.Begin()
			require.NoError(t, rootTx.Error)
			t.Cleanup(func() { _ = rootTx.Rollback().Error })
			require.NoError(t, rootTx.Exec("SELECT id FROM post WHERE id = ?::uuid FOR UPDATE", postID).Error)

			accessTx := db.Begin()
			require.NoError(t, accessTx.Error)
			t.Cleanup(func() { _ = accessTx.Rollback().Error })
			result := make(chan error, 1)
			go func() {
				err := test.require(postAuthorRaceContext(actorID), accessTx, spiceDB, postID)
				_ = accessTx.Rollback().Error
				result <- err
			}()
			requirePostMutationStillWaiting(t, result)

			removePostAuthorRelation(t, spiceDB, actorID, postID)
			require.NoError(t, rootTx.Commit().Error)
			require.Equal(t, test.wantCode, connect.CodeOf(<-result))
		})
	}
}

func TestPostMutationRejectsMemberTombstonedBeforePrincipalLockIntegration(t *testing.T) {
	db, spiceDB, actorID, actorMemberID, postID := seedPostAuthorityRaceFixtureWithSpiceDB(t)
	service := postintegration.NewPostDomainService(
		t, db, "", spiceDB,
		testutil.NewPostIdentityManager(testutil.PostIntegrationIdentity(actorID, "en")),
		testutil.NewPostContentBlockStore(t),
	)

	revokeTx := db.Begin()
	require.NoError(t, revokeTx.Error)
	require.NoError(t, revokeTx.Exec("SELECT id FROM member WHERE id = ?::uuid FOR UPDATE", actorMemberID).Error)

	result := make(chan error, 1)
	go func() {
		_, err := service.PublishPost(postAuthorRaceContext(actorID), connect.NewRequest(&managev1.PublishPostRequest{Id: postID}))
		result <- err
	}()
	requirePostMutationStillWaiting(t, result)
	require.NoError(t, revokeTx.Exec(`
		UPDATE member
		SET account_identity_id = NULL,
		    bio = NULL,
		    website = NULL,
		    social_links = '{}'::jsonb,
		    preferred_locale = NULL,
		    deleted_at = GREATEST(created_at, clock_timestamp()),
		    updated_at = GREATEST(created_at, clock_timestamp())
		WHERE id = ?::uuid
	`, actorMemberID).Error)
	require.NoError(t, revokeTx.Commit().Error)

	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(<-result))
	var status string
	require.NoError(t, db.Table("post").Select("status").Where("id = ?", postID).Scan(&status).Error)
	require.Equal(t, managev1.PostStatus_POST_STATUS_DRAFT.String(), status)
}

func TestCreatePostRejectsRoleDemotedBeforeIdentityLockIntegration(t *testing.T) {
	db := testutil.NewConcurrentPostIntegrationDB(t)
	actorID := testutil.PostIntegrationUUID()
	testutil.SeedPostIntegrationIdentity(t, db, actorID, "Create role race "+actorID[:8])
	spiceDB := testutil.PostIntegrationSpiceDB(t)
	testutil.GrantPostIntegrationRole(t, spiceDB, actorID, policyv1.Role.Author())
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM member WHERE id = ?::uuid", testutil.PostIntegrationMemberID(actorID)).Error
		_ = db.Exec("DELETE FROM kratos.identities WHERE id = ?::uuid", actorID).Error
	})

	service := postintegration.NewPostDomainService(
		t, db, "", spiceDB,
		testutil.NewPostIdentityManager(testutil.PostIntegrationIdentity(actorID, "en")),
		testutil.NewPostContentBlockStore(t),
	)
	demoteTx := db.Begin()
	require.NoError(t, demoteTx.Error)
	require.NoError(t, demoteTx.Exec("SELECT id FROM kratos.identities WHERE id = ?::uuid FOR UPDATE", actorID).Error)

	slug := "create-role-race-" + actorID
	result := make(chan error, 1)
	go func() {
		_, err := service.CreatePost(postAuthorRaceContext(actorID), connect.NewRequest(&managev1.CreatePostRequest{
			Title:    "Role race",
			Slug:     &slug,
			Document: testutil.EmptyPostDocument("en"),
		}))
		result <- err
	}()
	requirePostMutationStillWaiting(t, result)
	testutil.GrantPostIntegrationRole(t, spiceDB, actorID, policyv1.Role.User())
	require.NoError(t, demoteTx.Commit().Error)

	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(<-result))
	var count int64
	require.NoError(t, db.Table("post").Where("slug = ?", slug).Count(&count).Error)
	require.Zero(t, count)
}

func TestAddPostAuthorRejectsTargetRoleDemotedBeforeIdentityLockIntegration(t *testing.T) {
	db, spiceDB, actorID, _, postID := seedPostAuthorityRaceFixtureWithSpiceDB(t)
	targetID := testutil.PostIntegrationUUID()
	targetMemberID := testutil.PostIntegrationMemberID(targetID)
	testutil.SeedPostIntegrationIdentity(t, db, targetID, "Target role race "+targetID[:8])
	testutil.GrantPostIntegrationRole(t, spiceDB, targetID, policyv1.Role.Author())
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM member WHERE id = ?::uuid", targetMemberID).Error
		_ = db.Exec("DELETE FROM kratos.identities WHERE id = ?::uuid", targetID).Error
	})

	demoteTx := db.Begin()
	require.NoError(t, demoteTx.Error)
	require.NoError(t, demoteTx.Exec("SELECT id FROM kratos.identities WHERE id = ?::uuid FOR UPDATE", targetID).Error)
	result := make(chan error, 1)
	go func() {
		_, err := postintegration.NewPostDomainService(
			t, db, "", spiceDB,
			testutil.NewPostIdentityManager(testutil.PostIntegrationIdentity(actorID, "en")),
			testutil.NewPostContentBlockStore(t),
		).AddPostAuthor(postAuthorRaceContext(actorID), connect.NewRequest(&managev1.AddPostAuthorRequest{
			PostId: postID, MemberId: targetMemberID,
		}))
		result <- err
	}()
	requirePostMutationStillWaiting(t, result)
	testutil.GrantPostIntegrationRole(t, spiceDB, targetID, policyv1.Role.User())
	require.NoError(t, demoteTx.Commit().Error)

	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(<-result))
	var count int64
	require.NoError(t, db.Table("post_author").Where("post_id = ? AND member_id = ?", postID, targetMemberID).Count(&count).Error)
	require.Zero(t, count)
}

func seedPostAuthorityRaceFixture(t *testing.T) (*gorm.DB, string, string, string) {
	t.Helper()
	db := testutil.NewConcurrentPostIntegrationDB(t)
	actorID := testutil.PostIntegrationUUID()
	adminID := testutil.PostIntegrationUUID()
	testutil.SeedPostIntegrationIdentity(t, db, actorID, "Race author "+actorID[:8])
	testutil.SeedPostIntegrationIdentity(t, db, adminID, "Race admin "+adminID[:8])
	postID := testutil.PostIntegrationUUID()
	documentID := testutil.SeedPostContentDocument(t, db)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO post (id, status, comments_enabled, content_document_id, created_at, updated_at)
			VALUES (?::uuid, ?, TRUE, ?::uuid, NOW(), NOW())
		`, postID, managev1.PostStatus_POST_STATUS_DRAFT.String(), documentID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO post_author (post_id, member_id, created_at)
			VALUES (?::uuid, ?::uuid, NOW()), (?::uuid, ?::uuid, NOW())
		`, postID, testutil.PostIntegrationMemberID(actorID), postID, testutil.PostIntegrationMemberID(adminID)).Error
	}))
	t.Cleanup(func() {
		_ = db.Exec("DELETE FROM post WHERE id = ?::uuid", postID).Error
		_ = db.Exec("DELETE FROM content_document WHERE id = ?::uuid", documentID).Error
		_ = db.Exec("DELETE FROM member WHERE id IN (?::uuid, ?::uuid)", testutil.PostIntegrationMemberID(actorID), testutil.PostIntegrationMemberID(adminID)).Error
		_ = db.Exec("DELETE FROM kratos.identities WHERE id IN (?::uuid, ?::uuid)", actorID, adminID).Error
	})
	return db, actorID, testutil.PostIntegrationMemberID(actorID), postID
}

func seedPostAuthorityRaceFixtureWithSpiceDB(t *testing.T) (*gorm.DB, *auth.SpiceDBClient, string, string, string) {
	t.Helper()
	db, actorID, memberID, postID := seedPostAuthorityRaceFixture(t)
	spiceDB := testutil.PostIntegrationSpiceDB(t)
	testutil.GrantPostIntegrationRole(t, spiceDB, actorID, policyv1.Role.Author())
	actor, err := policyv1.NewAccountIdentityActor(actorID)
	require.NoError(t, err)
	policy, err := policyv1.Post.TouchPolicy(postID)
	require.NoError(t, err)
	author, err := policyv1.Post.TouchAuthor(postID, actor)
	require.NoError(t, err)
	_, err = spiceDB.ApplyRelationships(t.Context(), policy, author)
	require.NoError(t, err)
	return db, spiceDB, actorID, memberID, postID
}

func removePostAuthorRelation(t *testing.T, spiceDB *auth.SpiceDBClient, identityID, postID string) {
	t.Helper()
	actor, err := policyv1.NewAccountIdentityActor(identityID)
	require.NoError(t, err)
	mutation, err := policyv1.Post.DeleteAuthor(postID, actor)
	require.NoError(t, err)
	_, err = spiceDB.ApplyRelationships(t.Context(), mutation)
	require.NoError(t, err)
}

func postAuthorRaceContext(identityID string) context.Context {
	return auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(testutil.PostIntegrationMemberID(identityID)),
		SessionID: auth.SessionID(testutil.PostIntegrationUUID()), Authenticated: true,
	})
}

func requirePostMutationStillWaiting(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		require.FailNow(t, "post mutation returned before the Post root lock was released", "error: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
}
