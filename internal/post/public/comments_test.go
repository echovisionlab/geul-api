package public_test

import (
	"context"
	"testing"
	"time"

	fileref "github.com/echovisionlab/geul-api/internal/fileref"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/memberref"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/post/posttest"
	"github.com/echovisionlab/geul-api/internal/post/public"
	"github.com/echovisionlab/geul-api/internal/post/public/publictest"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type fakeFiles struct{}

func (fakeFiles) ResolveDisplayMedia(context.Context, []string) (map[string]*fileref.Delivery, error) {
	return map[string]*fileref.Delivery{}, nil
}
func (fakeFiles) ResolveAuthorizedContentBlockMedia(context.Context, uuid.UUID) ([]*contentv1.ContentBlockMediaItem, error) {
	return nil, nil
}

type fakeLocalizer struct{}

func (fakeLocalizer) ResolveSelection(context.Context, string, string, string, bool) (public.LocalizedContentSelection, error) {
	return public.LocalizedContentSelection{}, nil
}
func (fakeLocalizer) ResolveSelections(context.Context, string, []string, string, bool) (map[string]public.LocalizedContentSelection, error) {
	return map[string]public.LocalizedContentSelection{}, nil
}

type fakeMembers struct{}

func (fakeMembers) LoadPublicMemberSummaries(context.Context, []string) (map[string]*memberref.Summary, error) {
	return map[string]*memberref.Summary{}, nil
}

// harness is one public Post read test over the fake stores.
type harness struct {
	service *public.Service
	posts   *posttest.Store
	store   *publictest.Store
	checker *authztest.Checker
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	posts := posttest.NewStore()
	store, units := publictest.New(posts)
	checker := &authztest.Checker{}
	service, err := public.NewService(public.Deps{Units: units, SpiceDB: checker, Files: fakeFiles{}, Localizer: fakeLocalizer{}, Members: fakeMembers{}})
	require.NoError(t, err)
	return &harness{service: service, posts: posts, store: store, checker: checker}
}

func requireCode(t *testing.T, err error, code connect.Code) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, code, errs.Wrap(err).Code(), err.Error())
}

func signedIn(t *testing.T, identityID string) context.Context {
	return auth.WithUser(t.Context(), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(uuid.NewString()),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	})
}

func TestCommentReadsFollowPostVisibilityAndSetting(t *testing.T) {
	h := newHarness(t)
	publishedAt := time.Now().Add(-time.Hour)
	published := h.posts.PutPost(post.Post{Status: post.StatusPublished, PublishedAt: &publishedAt, CommentsEnabled: true}, "Public", "")
	draft := h.posts.PutPost(post.Post{CommentsEnabled: true}, "Draft", "")
	h.posts.PutComment(post.Comment{PostID: published.ID, Content: "Public thread"})
	root := h.posts.PutComment(post.Comment{PostID: draft.ID, Content: "Draft root"})
	reply := h.posts.PutComment(post.Comment{PostID: draft.ID, ParentID: &root.ID, Content: "Draft reply"})
	expired := time.Now().Add(-time.Minute)
	h.store.PutShareLink(publictest.ShareLink{Token: "token", Password: "secret", PostID: draft.ID})
	h.store.PutShareLink(publictest.ShareLink{Token: "expired", Password: "secret", PostID: draft.ID, ExpiresAt: &expired})
	author := uuid.NewString()
	h.checker.For(author).Allow(policyv1.Post.View(draft.ID))

	page, err := h.service.ListComments(t.Context(), public.ListPostCommentsQuery{PostID: published.ID})
	require.NoError(t, err, "anyone reads a published thread")
	require.Equal(t, "Public thread", page.Comments[0].Content)

	page, err = h.service.ListComments(signedIn(t, author), public.ListPostCommentsQuery{PostID: draft.ID})
	require.NoError(t, err, "an author previews a draft thread")
	require.Equal(t, root.ID, page.Comments[0].ID)
	require.Equal(t, reply.ID, page.Comments[0].Replies[0].ID)

	_, err = h.service.ListComments(signedIn(t, uuid.NewString()), public.ListPostCommentsQuery{PostID: draft.ID})
	requireCode(t, err, connect.CodeNotFound)
	for _, proof := range []struct{ token, password string }{{}, {"token", "wrong"}, {"unknown", "secret"}, {"expired", "secret"}} {
		_, err = h.service.ListComments(t.Context(), public.ListPostCommentsQuery{PostID: draft.ID, ShareToken: proof.token, SharePassword: proof.password})
		requireCode(t, err, connect.CodeNotFound)
		_, err = h.service.LoadMoreCommentReplies(t.Context(), public.LoadMorePostCommentRepliesQuery{
			PostID: draft.ID, CommentID: root.ID, ShareToken: proof.token, SharePassword: proof.password,
		})
		requireCode(t, err, connect.CodeNotFound)
	}
	replies, err := h.service.LoadMoreCommentReplies(t.Context(), public.LoadMorePostCommentRepliesQuery{
		PostID: draft.ID, CommentID: root.ID, ShareToken: "token", SharePassword: "secret",
	})
	require.NoError(t, err, "a valid share proof reads the draft replies")
	require.Equal(t, reply.ID, replies.Comments[0].ID)

	_, err = h.service.ListComments(t.Context(), public.ListPostCommentsQuery{PostID: "not-a-uuid"})
	requireCode(t, err, connect.CodeInvalidArgument)
	closed := h.posts.PutPost(post.Post{Status: post.StatusPublished, PublishedAt: &publishedAt}, "Closed", "")
	_, err = h.service.ListComments(t.Context(), public.ListPostCommentsQuery{PostID: closed.ID})
	requireCode(t, err, connect.CodeFailedPrecondition)
}
