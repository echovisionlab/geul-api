//go:build integration

package post_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/testutil/postintegration"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestListCommentsQueryBudgetStaysConstant(t *testing.T) {
	db := testutil.NewPostIntegrationDB(t)
	identityID := testutil.PostIntegrationUUID()
	testutil.SeedPostIntegrationIdentity(t, db, identityID, "Comment query member")
	memberID := testutil.PostIntegrationMemberID(identityID)
	postID := testutil.SeedPostBaseRow(t, db, managev1.PostStatus_POST_STATUS_DRAFT.String())
	require.NoError(t, db.Table("post").Where("id = ?", postID).
		Update("status", managev1.PostStatus_POST_STATUS_PUBLISHED.String()).Error)

	for index := 0; index < 12; index++ {
		comment := model.Comment{
			PostID: postID, MemberID: &memberID, Content: fmt.Sprintf("comment %02d", index),
			CreatedAt: time.Unix(1_700_000_000+int64(index), 0).UTC(),
		}
		require.NoError(t, db.Create(&comment).Error)
	}

	var queryCount atomic.Int64
	countedDB := db.Session(&gorm.Session{Logger: testutil.QueryCounter{
		Interface: db.Config.Logger,
		Count:     &queryCount,
	}})
	service := postintegration.NewPostCommentService(countedDB, "", nil)
	ctx := commentIntegrationUserContext(identityID)
	listCount := func(limit int32) int64 {
		queryCount.Store(0)
		response, err := service.ListCommentsByPost(ctx, connect.NewRequest(&managev1.ListCommentsByPostRequest{
			PostId: postID, Limit: limit,
		}))
		require.NoError(t, err)
		require.Len(t, response.Msg.Comments, int(limit))
		return queryCount.Load()
	}
	oneCommentQueries := listCount(1)
	twelveCommentQueries := listCount(12)
	require.Equal(t, oneCommentQueries, twelveCommentQueries)
	require.LessOrEqual(t, twelveCommentQueries, int64(6))

	require.NoError(t, db.Table("post").Where("id = ?", postID).
		Update("status", managev1.PostStatus_POST_STATUS_ARCHIVED.String()).Error)
	archivedResponse, err := service.ListCommentsByPost(ctx, connect.NewRequest(&managev1.ListCommentsByPostRequest{
		PostId: postID, Limit: 12,
	}))
	require.NoError(t, err)
	require.Len(t, archivedResponse.Msg.Comments, 12)
}

func TestCreateCommentRechecksPostPolicyAfterWaitingForRowLock(t *testing.T) {
	tests := []struct {
		name   string
		update structured.Fields
	}{
		{
			name:   "comments disabled",
			update: structured.Fields{"comments_enabled": false},
		},
		{
			name:   "post unpublished",
			update: structured.Fields{"status": managev1.PostStatus_POST_STATUS_DRAFT.String()},
		},
		{
			name:   "post archived",
			update: structured.Fields{"status": managev1.PostStatus_POST_STATUS_ARCHIVED.String()},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := testutil.NewConcurrentPostIntegrationDB(t)
			userID, postID := seedConcurrentCommentFixture(t, db)
			ctx := commentIntegrationUserContext(userID)

			lockTx := db.Begin()
			require.NoError(t, lockTx.Error)
			var post model.Post
			require.NoError(t, lockTx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ?", postID).
				First(&post).Error)

			result := make(chan error, 1)
			go func() {
				_, err := postintegration.NewPostCommentService(db, "", nil).CreateComment(
					ctx,
					connect.NewRequest(&managev1.CreateCommentRequest{
						PostId:  postID,
						Content: "must follow the committed post policy",
					}),
				)
				result <- err
			}()

			requireCallStillWaiting(t, result)
			require.NoError(t, lockTx.Model(&model.Post{}).
				Where("id = ?", postID).
				Updates(test.update).Error)
			require.NoError(t, lockTx.Commit().Error)

			err := requireCallResult(t, result)
			require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
			require.Equal(t, int64(0), testutil.CountPostIntegrationRows(t, db, "comment", "post_id = ?", postID))
		})
	}
}

func TestUpdateCommentRechecksDeletionAfterWaitingForRowLock(t *testing.T) {
	db := testutil.NewConcurrentPostIntegrationDB(t)
	userID, postID := seedConcurrentCommentFixture(t, db)
	memberID := testutil.PostIntegrationMemberID(userID)
	comment := model.Comment{PostID: postID, MemberID: &memberID, Content: "original"}
	require.NoError(t, db.Create(&comment).Error)

	lockTx := db.Begin()
	require.NoError(t, lockTx.Error)
	var locked model.Comment
	require.NoError(t, lockTx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", comment.ID).
		First(&locked).Error)

	result := make(chan error, 1)
	go func() {
		_, err := postintegration.NewPostCommentService(db, "", nil).UpdateComment(
			commentIntegrationUserContext(userID),
			connect.NewRequest(&managev1.UpdateCommentRequest{
				Id:      comment.ID,
				Content: "must not overwrite deletion",
			}),
		)
		result <- err
	}()

	requireCallStillWaiting(t, result)
	require.NoError(t, lockTx.Model(&model.Comment{}).
		Where("id = ?", comment.ID).
		Updates(structured.Fields{
			"is_deleted": true,
			"content":    "[This comment has been deleted]",
			"member_id":  nil,
		}).Error)
	require.NoError(t, lockTx.Commit().Error)

	err := requireCallResult(t, result)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.NoError(t, db.Where("id = ?", comment.ID).First(&comment).Error)
	require.True(t, comment.IsDeleted)
	require.Nil(t, comment.MemberID)
	require.Equal(t, "[This comment has been deleted]", comment.Content)
}

func TestCreateCommentRejectsMemberTombstonedBeforePrincipalLock(t *testing.T) {
	db := testutil.NewConcurrentPostIntegrationDB(t)
	userID, postID := seedConcurrentCommentFixture(t, db)
	memberID := testutil.PostIntegrationMemberID(userID)
	t.Cleanup(func() {
		_ = db.Exec(`
			UPDATE member
			SET account_identity_id = ?::uuid, deleted_at = NULL, updated_at = GREATEST(created_at, clock_timestamp())
			WHERE id = ?::uuid
		`, userID, memberID).Error
	})

	revokeTx := db.Begin()
	require.NoError(t, revokeTx.Error)
	require.NoError(t, revokeTx.Exec("SELECT id FROM member WHERE id = ?::uuid FOR UPDATE", memberID).Error)
	result := make(chan error, 1)
	go func() {
		_, err := postintegration.NewPostCommentService(db, "", nil).CreateComment(
			commentIntegrationUserContext(userID),
			connect.NewRequest(&managev1.CreateCommentRequest{PostId: postID, Content: "must be rejected"}),
		)
		result <- err
	}()
	requireCallStillWaiting(t, result)
	require.NoError(t, tombstoneMemberForAuthorityRace(revokeTx, memberID))
	require.NoError(t, revokeTx.Commit().Error)

	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(requireCallResult(t, result)))
	require.Equal(t, int64(0), testutil.CountPostIntegrationRows(t, db, "comment", "post_id = ?", postID))
}

func TestDeleteOwnCommentRejectsMemberTombstonedBeforePrincipalLock(t *testing.T) {
	db := testutil.NewConcurrentPostIntegrationDB(t)
	userID, postID := seedConcurrentCommentFixture(t, db)
	memberID := testutil.PostIntegrationMemberID(userID)
	comment := model.Comment{PostID: postID, MemberID: &memberID, Content: "preserve me"}
	require.NoError(t, db.Create(&comment).Error)
	t.Cleanup(func() {
		_ = db.Exec(`
			UPDATE member
			SET account_identity_id = ?::uuid, deleted_at = NULL, updated_at = GREATEST(created_at, clock_timestamp())
			WHERE id = ?::uuid
		`, userID, memberID).Error
	})

	revokeTx := db.Begin()
	require.NoError(t, revokeTx.Error)
	require.NoError(t, revokeTx.Exec("SELECT id FROM member WHERE id = ?::uuid FOR UPDATE", memberID).Error)
	result := make(chan error, 1)
	go func() {
		_, err := postintegration.NewPostCommentService(db, "", nil).DeleteComment(
			commentIntegrationUserContext(userID),
			connect.NewRequest(&managev1.DeleteCommentRequest{Id: comment.ID}),
		)
		result <- err
	}()
	requireCallStillWaiting(t, result)
	require.NoError(t, tombstoneMemberForAuthorityRace(revokeTx, memberID))
	require.NoError(t, revokeTx.Commit().Error)

	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(requireCallResult(t, result)))
	require.NoError(t, db.Where("id = ?", comment.ID).Take(&comment).Error)
	require.False(t, comment.IsDeleted)
	require.Equal(t, "preserve me", comment.Content)
}

func TestListCommentsIgnoresCursorFromAnotherPost(t *testing.T) {
	db := testutil.NewPostIntegrationDB(t)
	userID := testutil.PostIntegrationUUID()
	testutil.SeedPostIntegrationIdentity(t, db, userID, "Comment cursor user")
	memberID := testutil.PostIntegrationMemberID(userID)
	targetPostID := testutil.SeedPostBaseRow(t, db, managev1.PostStatus_POST_STATUS_DRAFT.String())
	otherPostID := testutil.SeedPostBaseRow(t, db, managev1.PostStatus_POST_STATUS_DRAFT.String())
	require.NoError(t, db.Table("post").Where("id IN ?", []string{targetPostID, otherPostID}).
		Update("status", managev1.PostStatus_POST_STATUS_PUBLISHED.String()).Error)

	target := model.Comment{
		PostID: targetPostID, MemberID: &memberID, Content: "target", CreatedAt: time.Unix(1_700_000_000, 0).UTC(),
	}
	other := model.Comment{
		PostID: otherPostID, MemberID: &memberID, Content: "other", CreatedAt: time.Unix(1_700_000_100, 0).UTC(),
	}
	require.NoError(t, db.Create(&target).Error)
	require.NoError(t, db.Create(&other).Error)

	response, err := postintegration.NewPostCommentService(db, "", nil).ListCommentsByPost(
		commentIntegrationUserContext(userID),
		connect.NewRequest(&managev1.ListCommentsByPostRequest{
			PostId: targetPostID,
			Cursor: &other.ID,
		}),
	)
	require.NoError(t, err)
	require.Len(t, response.Msg.Comments, 1)
	require.Equal(t, target.ID, response.Msg.Comments[0].Id)
}

func seedConcurrentCommentFixture(t *testing.T, db *gorm.DB) (string, string) {
	t.Helper()
	userID := testutil.PostIntegrationUUID()
	postID := testutil.PostIntegrationUUID()
	documentID := testutil.SeedPostContentDocument(t, db)
	testutil.SeedPostIntegrationIdentity(t, db, userID, "Concurrent comment "+userID[:8])
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO post (id, content_document_id, status, comments_enabled, created_at, updated_at)
			VALUES (?, ?, 'POST_STATUS_PUBLISHED', TRUE, NOW(), NOW())
		`, postID, documentID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO post_author (post_id, member_id, created_at)
			VALUES (?::uuid, ?::uuid, NOW())
		`, postID, testutil.PostIntegrationMemberID(userID)).Error
	}))
	t.Cleanup(func() {
		require.NoError(t, db.Exec("DELETE FROM post WHERE id = ?", postID).Error)
		require.NoError(t, db.Exec("DELETE FROM kratos.identities WHERE id = ?", userID).Error)
	})
	return userID, postID
}

func tombstoneMemberForAuthorityRace(tx *gorm.DB, memberID string) error {
	return tx.Exec(`
		UPDATE member
		SET account_identity_id = NULL,
		    bio = NULL,
		    website = NULL,
		    social_links = '{}'::jsonb,
		    preferred_locale = NULL,
		    deleted_at = GREATEST(created_at, clock_timestamp()),
		    updated_at = GREATEST(created_at, clock_timestamp())
		WHERE id = ?::uuid
	`, memberID).Error
}

func commentIntegrationUserContext(userID string) context.Context {
	return auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(userID),
		MemberID:      auth.MemberID(testutil.PostIntegrationMemberID(userID)),
		SessionID:     auth.SessionID(testutil.PostIntegrationUUID()),
		Authenticated: true,
	})
}

func requireCallStillWaiting(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		require.FailNow(t, "call returned before the authority row lock was released", "error: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
}

func requireCallResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		require.FailNow(t, "timed out waiting for service call")
		return nil
	}
}
