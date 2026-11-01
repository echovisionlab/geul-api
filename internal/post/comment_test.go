package post_test

import (
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/post"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// requireCommentAudit checks the one record of a comment change by actor.
func requireCommentAudit(t *testing.T, recorder *audittest.Recorder, actor principal, postID, commentID string, operation sharedtelemetry.AuditItemOperation) {
	t.Helper()
	records := recorder.Records()
	require.Len(t, records, 1)
	record := records[0]
	require.Equal(t, sharedtelemetry.AuditPostUpdated, record.Action)
	require.Equal(t, postID, record.TargetID)
	require.Equal(t, []string{"comments"}, record.ChangedFields)
	require.Equal(t, commentID, record.ItemID)
	require.Equal(t, operation, record.ItemOperation)
	require.Equal(t, actor.memberID, record.MemberID)
}

func TestCreateCommentStoresTheCommentAndAuditsIt(t *testing.T) {
	h := newHarness(t)
	commenter := h.member(newPrincipal(), "Commenter")
	value := h.seedPost("Commented", "", published)

	created, err := h.comments.CreateComment(commenter.context(t), post.CreateCommentCommand{PostID: value.ID, Content: "hello"})

	require.NoError(t, err)
	require.Equal(t, "hello", created.Content)
	require.Equal(t, commenter.memberID, *created.MemberID)
	require.Equal(t, "Commenter", created.Author.GetNickname())
	stored, ok := h.store.Comment(created.ID)
	require.True(t, ok)
	require.Equal(t, value.ID, stored.PostID)
	require.Nil(t, stored.ParentID)
	require.Equal(t, []string{commenter.identityID}, h.fence.Locked(), "the commenter's Identity is fenced")
	requireCommentAudit(t, h.audit, commenter, value.ID, created.ID, sharedtelemetry.AuditItemOperationCreated)
}

func TestCreateCommentRepliesUnderAParentUpToTheDepthLimit(t *testing.T) {
	h := newHarness(t)
	commenter := h.member(newPrincipal(), "Commenter")
	value := h.seedPost("Thread", "", published)
	parent := h.store.PutComment(post.Comment{PostID: value.ID, Content: "root"})
	for range 5 {
		parent = h.store.PutComment(post.Comment{PostID: value.ID, ParentID: &parent.ID, Content: "reply"})
	}
	ctx := commenter.context(t)

	_, err := h.comments.CreateComment(ctx, post.CreateCommentCommand{PostID: value.ID, ParentID: &parent.ID, Content: "too deep"})
	requireCode(t, err, connect.CodeFailedPrecondition, "maximum comment depth of 6 reached")

	shallow := h.store.PutComment(post.Comment{PostID: value.ID, Content: "another root"})
	reply, err := h.comments.CreateComment(ctx, post.CreateCommentCommand{PostID: value.ID, ParentID: &shallow.ID, Content: "reply"})
	require.NoError(t, err)
	require.Equal(t, shallow.ID, *reply.ParentID)

	other := h.seedPost("Other", "", published)
	_, err = h.comments.CreateComment(ctx, post.CreateCommentCommand{PostID: other.ID, ParentID: &shallow.ID, Content: "elsewhere"})
	requireCode(t, err, connect.CodeNotFound, "parent comment not found")
}

func TestCreateCommentRejectsAPostThatTakesNoComments(t *testing.T) {
	commenter := newPrincipal()
	for name, test := range map[string]struct {
		change  func(*post.Post)
		missing bool
		code    connect.Code
		message string
	}{
		"draft":    {change: func(value *post.Post) { value.CommentsEnabled = true }, code: connect.CodeFailedPrecondition, message: "cannot comment on unpublished post"},
		"disabled": {change: func(value *post.Post) { published(value); value.CommentsEnabled = false }, code: connect.CodeFailedPrecondition, message: "comments are disabled for this post"},
		"missing":  {missing: true, code: connect.CodeNotFound, message: "post not found"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.member(commenter, "Commenter")
			postID := "00000000-0000-4000-8000-000000000000"
			if !test.missing {
				postID = h.seedPost("Closed", "", test.change).ID
			}

			_, err := h.comments.CreateComment(commenter.context(t), post.CreateCommentCommand{PostID: postID, Content: "hello"})

			requireCode(t, err, test.code, test.message)
			require.Equal(t, mutation.StepRollback, lastStep(t, h.units))
			require.Empty(t, h.audit.Records())
		})
	}
}

func TestCreateCommentValidatesTheCommandAndThePrincipal(t *testing.T) {
	h := newHarness(t)
	commenter := h.member(newPrincipal(), "Commenter")
	value := h.seedPost("Validated", "", published)
	ctx := commenter.context(t)

	_, err := h.comments.CreateComment(t.Context(), post.CreateCommentCommand{PostID: value.ID, Content: "anonymous"})
	requireCode(t, err, connect.CodeUnauthenticated, "")
	_, err = h.comments.CreateComment(ctx, post.CreateCommentCommand{PostID: value.ID})
	requireCode(t, err, connect.CodeInvalidArgument, "")
	_, err = h.comments.CreateComment(ctx, post.CreateCommentCommand{PostID: value.ID, Content: strings.Repeat("x", 10001)})
	requireCode(t, err, connect.CodeInvalidArgument, "")

	h.fence.Deactivate(commenter.identityID)
	_, err = h.comments.CreateComment(ctx, post.CreateCommentCommand{PostID: value.ID, Content: "revoked"})
	requireCode(t, err, connect.CodePermissionDenied, "comment authority was revoked")
	require.Empty(t, h.audit.Records())
}

func TestCreateCommentRollsBackWhenItsAuditFails(t *testing.T) {
	h := newHarness(t, withAudit(&audittest.Recorder{Err: errDown}))
	commenter := h.member(newPrincipal(), "Commenter")
	value := h.seedPost("Audited", "", published)

	_, err := h.comments.CreateComment(commenter.context(t), post.CreateCommentCommand{PostID: value.ID, Content: "lost"})

	require.ErrorIs(t, err, errDown)
	require.Equal(t, mutation.StepRollback, lastStep(t, h.units))
	count, err := h.store.CountComments(t.Context(), value.ID, nil)
	require.NoError(t, err)
	require.Zero(t, count)
}

func TestUpdateCommentChangesOnlyTheAuthorsLiveComment(t *testing.T) {
	h := newHarness(t)
	author, other := h.member(newPrincipal(), "Author"), h.member(newPrincipal(), "Other")
	value := h.seedPost("Edited", "", published)
	comment := h.store.PutComment(post.Comment{PostID: value.ID, MemberID: &author.memberID, Content: "before"})
	removed := h.store.PutComment(post.Comment{PostID: value.ID, MemberID: &author.memberID, Content: "gone", IsDeleted: true})

	_, err := h.comments.UpdateComment(other.context(t), post.UpdateCommentCommand{ID: comment.ID, Content: "hijack"})
	requireCode(t, err, connect.CodePermissionDenied, "permission denied")
	_, err = h.comments.UpdateComment(author.context(t), post.UpdateCommentCommand{ID: removed.ID, Content: "revive"})
	requireCode(t, err, connect.CodeFailedPrecondition, "cannot update deleted comment")
	_, err = h.comments.UpdateComment(author.context(t), post.UpdateCommentCommand{ID: "00000000-0000-4000-8000-000000000000", Content: "nothing"})
	requireCode(t, err, connect.CodeNotFound, "comment not found")
	require.Empty(t, h.audit.Records())

	updated, err := h.comments.UpdateComment(author.context(t), post.UpdateCommentCommand{ID: comment.ID, Content: "after"})

	require.NoError(t, err)
	require.Equal(t, "after", updated.Content)
	require.Equal(t, "Author", updated.Author.GetNickname())
	stored, _ := h.store.Comment(comment.ID)
	require.Equal(t, "after", stored.Content)
	requireCommentAudit(t, h.audit, author, value.ID, comment.ID, sharedtelemetry.AuditItemOperationUpdated)
}

func TestDeleteCommentLetsTheAuthorOrAPostManagerSoftDeleteIt(t *testing.T) {
	author, manager, stranger := newPrincipal(), newPrincipal(), newPrincipal()
	for name, test := range map[string]struct {
		actor   principal
		allowed bool
	}{
		"the author":     {actor: author, allowed: true},
		"a Post manager": {actor: manager, allowed: true},
		"anyone else":    {actor: stranger},
	} {
		t.Run(name, func(t *testing.T) {
			checker := &authztest.Checker{}
			h := newHarness(t, withChecker(checker))
			for _, member := range []principal{author, manager, stranger} {
				h.member(member, "Member")
			}
			value := h.seedPost("Moderated", "", published)
			checker.For(manager.identityID).Allow(policyv1.Post.Manage(value.ID))
			comment := h.store.PutComment(post.Comment{PostID: value.ID, MemberID: &author.memberID, Content: "doomed"})

			err := h.comments.DeleteComment(test.actor.context(t), post.DeleteCommentCommand{ID: comment.ID})

			stored, _ := h.store.Comment(comment.ID)
			if !test.allowed {
				requireCode(t, err, connect.CodePermissionDenied, "")
				require.False(t, stored.IsDeleted)
				require.Empty(t, h.audit.Records())
				return
			}
			require.NoError(t, err)
			require.True(t, stored.IsDeleted)
			require.Equal(t, "[This comment has been deleted]", stored.Content)
			requireCommentAudit(t, h.audit, test.actor, value.ID, comment.ID, sharedtelemetry.AuditItemOperationDeleted)
		})
	}
}

func TestDeleteCommentRejectsARevokedPrincipalAndAMissingComment(t *testing.T) {
	h := newHarness(t)
	author := h.member(newPrincipal(), "Author")
	value := h.seedPost("Revoked", "", published)
	comment := h.store.PutComment(post.Comment{PostID: value.ID, MemberID: &author.memberID, Content: "kept"})

	err := h.comments.DeleteComment(author.context(t), post.DeleteCommentCommand{ID: "00000000-0000-4000-8000-000000000000"})
	requireCode(t, err, connect.CodeNotFound, "comment not found")

	h.fence.Deactivate(author.identityID)
	err = h.comments.DeleteComment(author.context(t), post.DeleteCommentCommand{ID: comment.ID})
	requireCode(t, err, connect.CodePermissionDenied, "comment authority was revoked")
	stored, _ := h.store.Comment(comment.ID)
	require.False(t, stored.IsDeleted)
	require.Empty(t, h.audit.Records())
}

func TestListCommentsPagesTopLevelCommentsWithTheirReplies(t *testing.T) {
	h := newHarness(t)
	reader := h.member(newPrincipal(), "Reader")
	value := h.seedPost("Listed", "", published)
	first := h.store.PutComment(post.Comment{PostID: value.ID, MemberID: &reader.memberID, Content: "first"})
	second := h.store.PutComment(post.Comment{PostID: value.ID, Content: "second"})
	reply := h.store.PutComment(post.Comment{PostID: value.ID, ParentID: &first.ID, Content: "reply"})
	ctx := reader.context(t)

	page, err := h.comments.ListCommentsByPost(ctx, post.ListCommentsQuery{PostID: value.ID, Limit: 1})

	require.NoError(t, err)
	require.EqualValues(t, 2, page.TotalCount)
	require.True(t, page.HasMore)
	require.Equal(t, first.ID, *page.NextCursor)
	require.Len(t, page.Comments, 1)
	require.Equal(t, "Reader", page.Comments[0].Author.GetNickname())
	require.Len(t, page.Comments[0].Replies, 1)
	require.Equal(t, reply.ID, page.Comments[0].Replies[0].ID)

	page, err = h.comments.ListCommentsByPost(ctx, post.ListCommentsQuery{PostID: value.ID, Limit: 1, Cursor: page.NextCursor})
	require.NoError(t, err)
	require.False(t, page.HasMore)
	require.Equal(t, second.ID, page.Comments[0].ID)

	replies, err := h.comments.LoadMoreReplies(ctx, post.LoadMoreRepliesQuery{CommentID: first.ID})
	require.NoError(t, err)
	require.EqualValues(t, 1, replies.TotalCount)
	require.Equal(t, reply.ID, replies.Comments[0].ID)
}

func TestListCommentsNeedsAViewableCommentablePost(t *testing.T) {
	reader := newPrincipal()
	checker := &authztest.Checker{}
	h := newHarness(t, withChecker(checker))
	h.member(reader, "Reader")
	draft := h.seedPost("Draft", "", func(value *post.Post) { value.CommentsEnabled = true })
	closed := h.seedPost("Closed", "", published, func(value *post.Post) { value.CommentsEnabled = false })
	ctx := reader.context(t)

	_, err := h.comments.ListCommentsByPost(ctx, post.ListCommentsQuery{PostID: draft.ID})
	requireCode(t, err, connect.CodeNotFound, "", "a draft the reader may not view stays hidden")
	_, err = h.comments.ListCommentsByPost(ctx, post.ListCommentsQuery{PostID: closed.ID})
	requireCode(t, err, connect.CodeFailedPrecondition, "comments are disabled for this post")

	checker.For(reader.identityID).Allow(policyv1.Post.View(draft.ID))
	page, err := h.comments.ListCommentsByPost(ctx, post.ListCommentsQuery{PostID: draft.ID})
	require.NoError(t, err)
	require.Zero(t, page.TotalCount)
	require.Empty(t, h.audit.Records(), "a read is not audited")
}
