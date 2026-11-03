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
	posts *Service
}

var _ openv1connect.PostServiceHandler = (*ConnectHandler)(nil)

// NewConnectHandler returns the Connect adapter of posts.
func NewConnectHandler(posts *Service) *ConnectHandler {
	return &ConnectHandler{posts: posts}
}

// render maps a use case's result with message; a failed use case answers
// its error.
func render[R, T any](result R, err error, message func(R) *T) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message(result)), nil
}

func (h *ConnectHandler) Get(ctx context.Context, req *connect.Request[openv1.GetPostRequest]) (*connect.Response[openv1.GetPostResponse], error) {
	view, err := h.posts.Get(ctx, GetPostQuery{
		Slug: req.Msg.Slug, ShareToken: req.Msg.GetShareToken(), SharePassword: req.Msg.GetSharePassword(),
		AcceptLanguage: req.Header().Get("Accept-Language"),
	})
	return render(view, err, postMessage)
}

func (h *ConnectHandler) ListComments(ctx context.Context, req *connect.Request[openv1.ListPostCommentsRequest]) (*connect.Response[openv1.ListPostCommentsResponse], error) {
	page, err := h.posts.ListComments(ctx, ListPostCommentsQuery{
		PostID: req.Msg.PostId, Limit: req.Msg.Limit, Cursor: req.Msg.Cursor, ReplyLimit: req.Msg.ReplyLimit,
		ShareToken: req.Msg.GetShareToken(), SharePassword: req.Msg.GetSharePassword(),
	})
	return render(page, err, commentsMessage)
}

func (h *ConnectHandler) LoadMoreCommentReplies(ctx context.Context, req *connect.Request[openv1.LoadMorePostCommentRepliesRequest]) (*connect.Response[openv1.ListPostCommentsResponse], error) {
	page, err := h.posts.LoadMoreCommentReplies(ctx, LoadMorePostCommentRepliesQuery{
		PostID: req.Msg.PostId, CommentID: req.Msg.CommentId, Limit: req.Msg.Limit, Cursor: req.Msg.Cursor,
		ShareToken: req.Msg.GetShareToken(), SharePassword: req.Msg.GetSharePassword(),
	})
	return render(page, err, commentsMessage)
}

func (h *ConnectHandler) List(ctx context.Context, req *connect.Request[openv1.ListPostsRequest]) (*connect.Response[openv1.ListPostsResponse], error) {
	page, err := h.posts.List(ctx, ListPostsQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(),
		Filters: req.Msg.Filters, Sorts: req.Msg.Sorts, AcceptLanguage: req.Header().Get("Accept-Language"),
	})
	return render(page, err, func(page SummaryPage) *openv1.ListPostsResponse {
		return &openv1.ListPostsResponse{Posts: summaryMessages(page.Summaries), Pagination: page.Pagination}
	})
}

func (h *ConnectHandler) ListMapFeatures(ctx context.Context, req *connect.Request[openv1.ListPostMapFeaturesRequest]) (*connect.Response[openv1.ListPostMapFeaturesResponse], error) {
	features, err := h.posts.ListMapFeatures(ctx, ListPostMapFeaturesQuery{
		Viewport: mapViewport(req.Msg.Viewport), Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
		AcceptLanguage: req.Header().Get("Accept-Language"),
	})
	return render(features, err, mapFeaturesMessage)
}

func (h *ConnectHandler) Search(ctx context.Context, req *connect.Request[openv1.SearchPostsRequest]) (*connect.Response[openv1.SearchPostsResponse], error) {
	summaries, err := h.posts.Search(ctx, SearchPostsQuery{
		Query: req.Msg.Query, Limit: req.Msg.Limit, AcceptLanguage: req.Header().Get("Accept-Language"),
	})
	return render(summaries, err, func(summaries []Summary) *openv1.SearchPostsResponse {
		return &openv1.SearchPostsResponse{Posts: summaryMessages(summaries)}
	})
}
