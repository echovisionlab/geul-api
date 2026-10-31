package public

import (
	"context"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
)

// ConnectHandler serves the public Post RPCs.
type ConnectHandler struct {
	openv1connect.UnimplementedPostServiceHandler
	posts *Service
}

var _ openv1connect.PostServiceHandler = (*ConnectHandler)(nil)

// NewConnectHandler returns the Connect adapter of posts.
func NewConnectHandler(posts *Service) *ConnectHandler {
	return &ConnectHandler{posts: posts}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

func (h *ConnectHandler) Get(ctx context.Context, req *connect.Request[openv1.GetPostRequest]) (*connect.Response[openv1.GetPostResponse], error) {
	return respond(h.posts.Get(ctx, GetPostQuery{
		Slug: req.Msg.Slug, ShareToken: req.Msg.GetShareToken(), SharePassword: req.Msg.GetSharePassword(),
		AcceptLanguage: req.Header().Get("Accept-Language"),
	}))
}

func (h *ConnectHandler) ListComments(ctx context.Context, req *connect.Request[openv1.ListPostCommentsRequest]) (*connect.Response[openv1.ListPostCommentsResponse], error) {
	return respond(h.posts.ListComments(ctx, ListPostCommentsQuery{
		PostID: req.Msg.PostId, Limit: req.Msg.Limit, Cursor: req.Msg.Cursor, ReplyLimit: req.Msg.ReplyLimit,
		ShareToken: req.Msg.GetShareToken(), SharePassword: req.Msg.GetSharePassword(),
	}))
}

func (h *ConnectHandler) LoadMoreCommentReplies(ctx context.Context, req *connect.Request[openv1.LoadMorePostCommentRepliesRequest]) (*connect.Response[openv1.ListPostCommentsResponse], error) {
	return respond(h.posts.LoadMoreCommentReplies(ctx, LoadMorePostCommentRepliesQuery{
		PostID: req.Msg.PostId, CommentID: req.Msg.CommentId, Limit: req.Msg.Limit, Cursor: req.Msg.Cursor,
		ShareToken: req.Msg.GetShareToken(), SharePassword: req.Msg.GetSharePassword(),
	}))
}

func (h *ConnectHandler) List(ctx context.Context, req *connect.Request[openv1.ListPostsRequest]) (*connect.Response[openv1.ListPostsResponse], error) {
	return respond(h.posts.List(ctx, ListPostsQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(),
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts, AcceptLanguage: req.Header().Get("Accept-Language"),
	}))
}

func (h *ConnectHandler) ListMapFeatures(ctx context.Context, req *connect.Request[openv1.ListPostMapFeaturesRequest]) (*connect.Response[openv1.ListPostMapFeaturesResponse], error) {
	return respond(h.posts.ListMapFeatures(ctx, ListPostMapFeaturesQuery{
		Viewport: req.Msg.Viewport, Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
		AcceptLanguage: req.Header().Get("Accept-Language"),
	}))
}

func (h *ConnectHandler) Search(ctx context.Context, req *connect.Request[openv1.SearchPostsRequest]) (*connect.Response[openv1.SearchPostsResponse], error) {
	return respond(h.posts.Search(ctx, SearchPostsQuery{
		Query: req.Msg.Query, Limit: req.Msg.Limit, AcceptLanguage: req.Header().Get("Accept-Language"),
	}))
}
