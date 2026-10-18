//go:build integration

package public_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	postadapter "github.com/echovisionlab/geul-api/internal/adapters/post"
	postruntime "github.com/echovisionlab/geul-api/internal/adapters/post/runtime"
	referencecatalogadapter "github.com/echovisionlab/geul-api/internal/adapters/referencecatalog"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/model"
	postpublic "github.com/echovisionlab/geul-api/internal/post/public"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCommentReadsFollowPostVisibilityAndSettingIntegration(t *testing.T) {
	stack, err := testutil.StartBackendIntegrationStack(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stack.Close()) })
	db := stack.Postgres.DB
	service := postpublic.NewPostService(db, "", stack.SpiceDBClient, referenceSearchFileService{},
		postadapter.NewLocalization(), referencecatalogadapter.PublicMapPlaces{},
		postadapter.NewMemberSummaries(db, ""), postruntime.ShareLinks{})

	identityID := uuid.NewString()
	testutil.SeedPostIntegrationIdentity(t, db, identityID, "Comment preview author")
	testutil.GrantPostIntegrationRole(t, stack.SpiceDBClient, identityID, policyv1.Role.Admin())
	publicID := testutil.SeedPostBaseRow(t, db, managev1.PostStatus_POST_STATUS_PUBLISHED.String())
	draftID := testutil.SeedPostBaseRow(t, db, managev1.PostStatus_POST_STATUS_DRAFT.String())
	root := model.Comment{PostID: draftID, Content: "Existing thread in a draft"}
	require.NoError(t, db.Create(&root).Error)
	reply := model.Comment{PostID: draftID, ParentID: &root.ID, Content: "Existing reply"}
	require.NoError(t, db.Create(&reply).Error)
	publicComment := model.Comment{PostID: publicID, Content: "Public thread"}
	require.NoError(t, db.Create(&publicComment).Error)
	password := "comment-preview-password"
	hash, err := crypto.NewPasswordHasher(nil).Hash(password)
	require.NoError(t, err)
	expires := time.Now().Add(time.Hour)
	link := model.ShareLink{Token: uuid.NewString(), EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_POST.String(),
		EntityID: draftID, PasswordHash: &hash, ExpiresAt: &expires}
	require.NoError(t, db.Create(&link).Error)

	t.Run("anonymous published thread", func(t *testing.T) {
		response, err := service.ListComments(context.Background(), connect.NewRequest(&openv1.ListPostCommentsRequest{PostId: publicID}))
		require.NoError(t, err)
		require.Equal(t, "Public thread", response.Msg.Comments[0].Content)
	})
	t.Run("author draft preview", func(t *testing.T) {
		response, err := service.ListComments(testutil.PostIntegrationContext(identityID),
			connect.NewRequest(&openv1.ListPostCommentsRequest{PostId: draftID}))
		require.NoError(t, err)
		require.Equal(t, root.ID, response.Msg.Comments[0].Id)
		require.Equal(t, reply.ID, response.Msg.Comments[0].Replies[0].Id)
	})
	t.Run("unrelated authenticated member cannot read a draft thread", func(t *testing.T) {
		stranger := uuid.NewString()
		testutil.SeedPostIntegrationIdentity(t, db, stranger, "Unrelated comment reader")
		testutil.GrantPostIntegrationRole(t, stack.SpiceDBClient, stranger, policyv1.Role.User())
		_, err := service.ListComments(testutil.PostIntegrationContext(stranger), connect.NewRequest(&openv1.ListPostCommentsRequest{PostId: draftID}))
		require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	})

	t.Run("draft and reply access needs the same valid share proof", func(t *testing.T) {
		for _, proof := range []struct{ token, password string }{{}, {link.Token, "wrong"}, {"unknown", password}} {
			_, err := service.ListComments(context.Background(), connect.NewRequest(&openv1.ListPostCommentsRequest{
				PostId: draftID, ShareToken: &proof.token, SharePassword: &proof.password,
			}))
			require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
			_, err = service.LoadMoreCommentReplies(context.Background(), connect.NewRequest(&openv1.LoadMorePostCommentRepliesRequest{
				PostId: draftID, CommentId: root.ID, ShareToken: &proof.token, SharePassword: &proof.password,
			}))
			require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
		}
		response, err := service.LoadMoreCommentReplies(context.Background(), connect.NewRequest(&openv1.LoadMorePostCommentRepliesRequest{
			PostId: draftID, CommentId: root.ID, ShareToken: &link.Token, SharePassword: &password,
		}))
		require.NoError(t, err)
		require.Equal(t, reply.ID, response.Msg.Comments[0].Id)
		_, err = service.LoadMoreCommentReplies(context.Background(), connect.NewRequest(&openv1.LoadMorePostCommentRepliesRequest{
			PostId: publicID, CommentId: root.ID,
		}))
		require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	})
	t.Run("expired share cannot read the draft thread", func(t *testing.T) {
		require.NoError(t, db.Model(&link).Update("expires_at", time.Now().Add(-time.Minute)).Error)
		_, err := service.ListComments(context.Background(), connect.NewRequest(&openv1.ListPostCommentsRequest{
			PostId: draftID, ShareToken: &link.Token, SharePassword: &password,
		}))
		require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	})
	t.Run("disabled comments block reads and survive re-enabling", func(t *testing.T) {
		require.NoError(t, db.Table("post").Where("id = ?", publicID).Update("comments_enabled", false).Error)
		_, err := service.ListComments(context.Background(), connect.NewRequest(&openv1.ListPostCommentsRequest{PostId: publicID}))
		require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
		_, err = service.LoadMoreCommentReplies(context.Background(), connect.NewRequest(&openv1.LoadMorePostCommentRepliesRequest{
			PostId: publicID, CommentId: publicComment.ID,
		}))
		require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
		require.NoError(t, db.Table("post").Where("id = ?", publicID).Update("comments_enabled", true).Error)
		response, err := service.ListComments(context.Background(), connect.NewRequest(&openv1.ListPostCommentsRequest{PostId: publicID}))
		require.NoError(t, err)
		require.Equal(t, publicComment.ID, response.Msg.Comments[0].Id)
	})
}
