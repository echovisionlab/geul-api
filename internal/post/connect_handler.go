package post

import (
	"context"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

// ConnectHandler serves the Post management RPCs.
type ConnectHandler struct {
	managev1connect.UnimplementedPostServiceHandler
	posts *Service
}

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

func deleted(err error) (*connect.Response[managev1.DeleteResponse], error) {
	return respond(&managev1.DeleteResponse{Success: true}, err)
}

func page(request interface {
	GetLimit() int32
	GetOffset() int32
}) Page {
	return Page{Limit: request.GetLimit(), Offset: request.GetOffset()}
}

func (h *ConnectHandler) GetPost(ctx context.Context, req *connect.Request[managev1.GetPostRequest]) (*connect.Response[managev1.Post], error) {
	return respond(h.posts.GetPost(ctx, GetPostQuery{ID: req.Msg.Id}))
}

func (h *ConnectHandler) GetPostEditorDocument(ctx context.Context, req *connect.Request[managev1.GetPostEditorDocumentRequest]) (*connect.Response[managev1.GetPostEditorDocumentResponse], error) {
	return respond(h.posts.GetPostEditorDocument(ctx, GetPostEditorDocumentQuery{PostID: req.Msg.PostId, Locale: req.Msg.Locale}))
}

func (h *ConnectHandler) CreatePost(ctx context.Context, req *connect.Request[managev1.CreatePostRequest]) (*connect.Response[managev1.Post], error) {
	msg := req.Msg
	return respond(h.posts.CreatePost(ctx, CreatePostCommand{
		Title: msg.Title, Slug: msg.Slug, Summary: msg.Summary, CategoryIDs: msg.CategoryIds, TagIDs: msg.TagIds,
		CommentsEnabled: msg.CommentsEnabled, MapPlaceID: msg.MapPlaceId, SourceLocale: msg.SourceLocale,
		Document: msg.Document, AcceptLanguage: req.Header().Get("Accept-Language"),
	}))
}

func (h *ConnectHandler) UpdatePost(ctx context.Context, req *connect.Request[managev1.UpdatePostRequest]) (*connect.Response[managev1.UpdatePostResponse], error) {
	msg := req.Msg
	return respond(h.posts.UpdatePost(ctx, UpdatePostCommand{
		ID: msg.Id, Slug: msg.Slug, CommentsEnabled: msg.CommentsEnabled, MapPlaceID: msg.MapPlaceId,
		DocumentLayout: msg.DocumentLayout, ExpectedConfigurationRevision: msg.ExpectedConfigurationRevision,
	}))
}

func (h *ConnectHandler) DeletePost(ctx context.Context, req *connect.Request[managev1.DeletePostRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return deleted(h.posts.DeletePost(ctx, DeletePostCommand{ID: req.Msg.Id}))
}

func (h *ConnectHandler) PublishPost(ctx context.Context, req *connect.Request[managev1.PublishPostRequest]) (*connect.Response[managev1.PostLifecycleMutationResponse], error) {
	return respond(h.posts.PublishPost(ctx, PostLifecycleCommand{ID: req.Msg.Id}))
}

func (h *ConnectHandler) UnpublishPost(ctx context.Context, req *connect.Request[managev1.UnpublishPostRequest]) (*connect.Response[managev1.PostLifecycleMutationResponse], error) {
	return respond(h.posts.UnpublishPost(ctx, PostLifecycleCommand{ID: req.Msg.Id}))
}

func (h *ConnectHandler) ArchivePost(ctx context.Context, req *connect.Request[managev1.ArchivePostRequest]) (*connect.Response[managev1.PostLifecycleMutationResponse], error) {
	return respond(h.posts.ArchivePost(ctx, PostLifecycleCommand{ID: req.Msg.Id}))
}

func (h *ConnectHandler) SchedulePost(ctx context.Context, req *connect.Request[managev1.SchedulePostRequest]) (*connect.Response[managev1.PostLifecycleMutationResponse], error) {
	command := SchedulePostCommand{ID: req.Msg.Id, TimeZone: req.Msg.ScheduledTimeZone}
	if at := req.Msg.ScheduledAt; at != nil {
		if err := at.CheckValid(); err != nil {
			return nil, errs.Wrap(domainerr.InvalidField("scheduled_at", "must be a valid timestamp").WithCause(err))
		}
		scheduledAt := at.AsTime()
		command.ScheduledAt = &scheduledAt
	}
	return respond(h.posts.SchedulePost(ctx, command))
}

func (h *ConnectHandler) CancelPostSchedule(ctx context.Context, req *connect.Request[managev1.CancelPostScheduleRequest]) (*connect.Response[managev1.PostLifecycleMutationResponse], error) {
	return respond(h.posts.CancelPostSchedule(ctx, PostLifecycleCommand{ID: req.Msg.Id}))
}

func (h *ConnectHandler) RepublishPost(ctx context.Context, req *connect.Request[managev1.RepublishPostRequest]) (*connect.Response[managev1.PostLifecycleMutationResponse], error) {
	return respond(h.posts.RepublishPost(ctx, PostLifecycleCommand{ID: req.Msg.Id}))
}

func (h *ConnectHandler) ListPostsAdmin(ctx context.Context, req *connect.Request[managev1.ListPostsAdminRequest]) (*connect.Response[managev1.ListPostsAdminResponse], error) {
	return respond(h.posts.ListPostsAdmin(ctx, ListPostsQuery{Page: page(req.Msg.GetPagination()), Filters: req.Msg.Filters, Sorts: req.Msg.Sorts}))
}

func (h *ConnectHandler) ListMyPosts(ctx context.Context, req *connect.Request[managev1.ListMyPostsRequest]) (*connect.Response[managev1.ListMyPostsResponse], error) {
	return respond(h.posts.ListMyPosts(ctx, ListPostsQuery{Page: page(req.Msg.GetPagination()), Filters: req.Msg.Filters, Sorts: req.Msg.Sorts}))
}

func (h *ConnectHandler) SetPostFeaturedImage(ctx context.Context, req *connect.Request[managev1.SetPostFeaturedImageRequest]) (*connect.Response[managev1.SetPostFeaturedImageResponse], error) {
	return respond(h.posts.SetPostFeaturedImage(ctx, SetPostFeaturedImageCommand{PostID: req.Msg.PostId, FileID: req.Msg.FileId}))
}

func (h *ConnectHandler) DeletePostFeaturedImage(ctx context.Context, req *connect.Request[managev1.DeletePostFeaturedImageRequest]) (*connect.Response[managev1.OgAssetDeleteResponse], error) {
	return respond(h.posts.DeletePostFeaturedImage(ctx, DeletePostFeaturedImageCommand{PostID: req.Msg.PostId}))
}

func (h *ConnectHandler) ListPostParticipants(ctx context.Context, req *connect.Request[managev1.ListPostParticipantsRequest]) (*connect.Response[managev1.ListPostParticipantsResponse], error) {
	return respond(h.posts.ListPostParticipants(ctx, PostParticipantsQuery{PostID: req.Msg.PostId}))
}

func (h *ConnectHandler) AddPostAuthor(ctx context.Context, req *connect.Request[managev1.AddPostAuthorRequest]) (*connect.Response[managev1.PostParticipant], error) {
	return respond(h.posts.AddPostAuthor(ctx, PostParticipantCommand{PostID: req.Msg.PostId, MemberID: req.Msg.MemberId}))
}

func (h *ConnectHandler) RemovePostAuthor(ctx context.Context, req *connect.Request[managev1.RemovePostAuthorRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return deleted(h.posts.RemovePostAuthor(ctx, PostParticipantCommand{PostID: req.Msg.PostId, MemberID: req.Msg.MemberId}))
}

func (h *ConnectHandler) AddPostCollaborator(ctx context.Context, req *connect.Request[managev1.AddPostCollaboratorRequest]) (*connect.Response[managev1.PostParticipant], error) {
	return respond(h.posts.AddPostCollaborator(ctx, PostParticipantCommand{PostID: req.Msg.PostId, MemberID: req.Msg.MemberId}))
}

func (h *ConnectHandler) RemovePostCollaborator(ctx context.Context, req *connect.Request[managev1.RemovePostCollaboratorRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return deleted(h.posts.RemovePostCollaborator(ctx, PostParticipantCommand{PostID: req.Msg.PostId, MemberID: req.Msg.MemberId}))
}

func (h *ConnectHandler) ListPostVersions(ctx context.Context, req *connect.Request[managev1.ListPostVersionsRequest]) (*connect.Response[managev1.ListPostVersionsResponse], error) {
	return respond(h.posts.ListPostVersions(ctx, ListPostVersionsQuery{PostID: req.Msg.PostId, Page: page(req.Msg.GetPagination())}))
}

func (h *ConnectHandler) RestorePostVersion(ctx context.Context, req *connect.Request[managev1.RestorePostVersionRequest]) (*connect.Response[managev1.Post], error) {
	return respond(h.posts.RestorePostVersion(ctx, RestorePostVersionCommand{PostID: req.Msg.PostId, VersionID: req.Msg.VersionId}))
}

func (h *ConnectHandler) CheckSlugAvailable(ctx context.Context, req *connect.Request[managev1.CheckSlugAvailableRequest]) (*connect.Response[managev1.CheckSlugAvailableResponse], error) {
	return respond(h.posts.CheckSlugAvailable(ctx, CheckSlugAvailableQuery{Slug: req.Msg.Slug, ExcludePostID: req.Msg.ExcludePostId}))
}

// InternalConnectHandler serves the Post collaboration RPCs editor-collab
// calls.
type InternalConnectHandler struct {
	intrav1connect.UnimplementedInternalPostServiceHandler
	posts *Service
}

// NewInternalConnectHandler returns the internal Connect adapter of posts.
func NewInternalConnectHandler(posts *Service) *InternalConnectHandler {
	return &InternalConnectHandler{posts: posts}
}

func (h *InternalConnectHandler) LoadPostBlockDocument(ctx context.Context, req *connect.Request[intrav1.LoadPostBlockDocumentRequest]) (*connect.Response[intrav1.LoadPostBlockDocumentResponse], error) {
	query := LoadPostBlockDocumentQuery{PostID: req.Msg.PostId, Locale: req.Msg.Locale}
	if principal := req.Msg.Principal; principal != nil {
		sessionID := principal.GetSessionId()
		query.SessionID = &sessionID
	}
	return respond(h.posts.LoadPostBlockDocument(ctx, query))
}

func (h *InternalConnectHandler) ApplyPostBlockBatch(ctx context.Context, req *connect.Request[intrav1.ApplyPostBlockBatchRequest]) (*connect.Response[intrav1.ApplyPostBlockBatchResponse], error) {
	msg := req.Msg
	return respond(h.posts.ApplyPostBlockBatch(ctx, ApplyPostBlockBatchCommand{
		PostID: msg.PostId, Locale: msg.Locale, Batch: msg.Batch,
		AffectedLocaleValues: msg.AffectedLocaleValues, ExpectedTargetRevision: msg.ExpectedTargetRevision,
	}))
}

func (h *InternalConnectHandler) UpdatePostLocaleMetadata(ctx context.Context, req *connect.Request[intrav1.UpdatePostLocaleMetadataRequest]) (*connect.Response[intrav1.UpdatePostLocaleMetadataResponse], error) {
	msg := req.Msg
	return respond(h.posts.UpdatePostLocaleMetadata(ctx, UpdatePostLocaleMetadataCommand{
		PostID: msg.PostId, Locale: msg.Locale, TitleChange: msg.TitleChange, SummaryChange: msg.SummaryChange,
		ExpectedRevision: msg.ExpectedRevision, ExpectedTargetRevision: msg.ExpectedTargetRevision,
		ContributorMemberIDs: msg.ContributorMemberIds,
	}))
}

func postStringIDs(list *intrav1.PostStringIdList) *[]string {
	if list == nil {
		return nil
	}
	ids := list.GetIds()
	return &ids
}

func (h *InternalConnectHandler) UpdatePostDocumentMetadata(ctx context.Context, req *connect.Request[intrav1.UpdatePostDocumentMetadataRequest]) (*connect.Response[intrav1.UpdatePostDocumentMetadataResponse], error) {
	msg := req.Msg
	return respond(h.posts.UpdatePostDocumentMetadata(ctx, UpdatePostDocumentMetadataCommand{
		PostID: msg.PostId, Locale: msg.Locale, CategoryIDs: postStringIDs(msg.CategoryIds), TagIDs: postStringIDs(msg.TagIds),
		ExpectedRevision: msg.ExpectedRevision, ContributorMemberIDs: msg.ContributorMemberIds,
	}))
}

func (h *InternalConnectHandler) CreatePostVersionCheckpoint(ctx context.Context, req *connect.Request[intrav1.CreatePostVersionCheckpointRequest]) (*connect.Response[intrav1.CreatePostVersionCheckpointResponse], error) {
	msg := req.Msg
	return respond(h.posts.CreatePostVersionCheckpoint(ctx, CreatePostVersionCheckpointCommand{
		PostID: msg.PostId, Locale: msg.Locale, ExpectedRevision: msg.ExpectedRevision, ContributorMemberIDs: msg.ContributorMemberIds,
	}))
}

// CommentConnectHandler serves the Post comment RPCs.
type CommentConnectHandler struct {
	managev1connect.UnimplementedCommentServiceHandler
	comments *CommentService
}

// NewCommentConnectHandler returns the Connect adapter of comments.
func NewCommentConnectHandler(comments *CommentService) *CommentConnectHandler {
	return &CommentConnectHandler{comments: comments}
}

func (h *CommentConnectHandler) ListCommentsByPost(ctx context.Context, req *connect.Request[managev1.ListCommentsByPostRequest]) (*connect.Response[managev1.ListCommentsByPostResponse], error) {
	msg := req.Msg
	return respond(h.comments.ListCommentsByPost(ctx, ListCommentsQuery{PostID: msg.PostId, Limit: msg.Limit, Cursor: msg.Cursor, ReplyLimit: msg.ReplyLimit}))
}

func (h *CommentConnectHandler) LoadMoreReplies(ctx context.Context, req *connect.Request[managev1.LoadMoreRepliesRequest]) (*connect.Response[managev1.LoadMoreRepliesResponse], error) {
	return respond(h.comments.LoadMoreReplies(ctx, LoadMoreRepliesQuery{CommentID: req.Msg.CommentId, Limit: req.Msg.Limit, Cursor: req.Msg.Cursor}))
}

func (h *CommentConnectHandler) CreateComment(ctx context.Context, req *connect.Request[managev1.CreateCommentRequest]) (*connect.Response[managev1.CreateCommentResponse], error) {
	return respond(h.comments.CreateComment(ctx, CreateCommentCommand{PostID: req.Msg.PostId, ParentID: req.Msg.ParentId, Content: req.Msg.Content}))
}

func (h *CommentConnectHandler) UpdateComment(ctx context.Context, req *connect.Request[managev1.UpdateCommentRequest]) (*connect.Response[managev1.UpdateCommentResponse], error) {
	return respond(h.comments.UpdateComment(ctx, UpdateCommentCommand{ID: req.Msg.Id, Content: req.Msg.Content}))
}

func (h *CommentConnectHandler) DeleteComment(ctx context.Context, req *connect.Request[managev1.DeleteCommentRequest]) (*connect.Response[managev1.DeleteCommentResponse], error) {
	return respond(&managev1.DeleteCommentResponse{Success: true}, h.comments.DeleteComment(ctx, DeleteCommentCommand{ID: req.Msg.Id}))
}
