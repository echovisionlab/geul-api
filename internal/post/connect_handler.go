package post

import (
	"context"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// ConnectHandler serves the Post management RPCs.
type ConnectHandler struct {
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

// render maps a use case's result with message; a failed use case answers
// its error.
func render[R, T any](result R, err error, message func(R) *T) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message(result)), nil
}

func (h *ConnectHandler) GetPost(ctx context.Context, req *connect.Request[managev1.GetPostRequest]) (*connect.Response[managev1.Post], error) {
	view, err := h.posts.GetPost(ctx, GetPostQuery{ID: req.Msg.Id})
	return render(view, err, PostMessage)
}

func (h *ConnectHandler) GetPostEditorDocument(ctx context.Context, req *connect.Request[managev1.GetPostEditorDocumentRequest]) (*connect.Response[managev1.GetPostEditorDocumentResponse], error) {
	document, err := h.posts.GetPostEditorDocument(ctx, GetPostEditorDocumentQuery{PostID: req.Msg.PostId, Locale: req.Msg.Locale})
	return render(document, err, editorDocumentMessage)
}

func (h *ConnectHandler) CreatePost(ctx context.Context, req *connect.Request[managev1.CreatePostRequest]) (*connect.Response[managev1.Post], error) {
	msg := req.Msg
	view, err := h.posts.CreatePost(ctx, CreatePostCommand{
		Title: msg.Title, Slug: msg.Slug, Summary: msg.Summary, CategoryIDs: msg.CategoryIds, TagIDs: msg.TagIds,
		CommentsEnabled: msg.CommentsEnabled, MapPlaceID: msg.MapPlaceId, SourceLocale: msg.SourceLocale,
		Document: msg.Document, AcceptLanguage: req.Header().Get("Accept-Language"),
	})
	return render(view, err, PostMessage)
}

func (h *ConnectHandler) UpdatePost(ctx context.Context, req *connect.Request[managev1.UpdatePostRequest]) (*connect.Response[managev1.UpdatePostResponse], error) {
	msg := req.Msg
	update, err := h.posts.UpdatePost(ctx, UpdatePostCommand{
		ID: msg.Id, Slug: msg.Slug, CommentsEnabled: msg.CommentsEnabled, MapPlaceID: msg.MapPlaceId,
		DocumentLayout: DocumentLayoutFromMessage(msg.DocumentLayout), ExpectedConfigurationRevision: msg.ExpectedConfigurationRevision,
	})
	return render(update, err, SettingsUpdateMessage)
}

func (h *ConnectHandler) DeletePost(ctx context.Context, req *connect.Request[managev1.DeletePostRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return deleted(h.posts.DeletePost(ctx, DeletePostCommand{ID: req.Msg.Id}))
}

func (h *ConnectHandler) PublishPost(ctx context.Context, req *connect.Request[managev1.PublishPostRequest]) (*connect.Response[managev1.PostLifecycleMutationResponse], error) {
	lifecycle, err := h.posts.PublishPost(ctx, PostLifecycleCommand{ID: req.Msg.Id})
	return render(lifecycle, err, LifecycleMessage)
}

func (h *ConnectHandler) UnpublishPost(ctx context.Context, req *connect.Request[managev1.UnpublishPostRequest]) (*connect.Response[managev1.PostLifecycleMutationResponse], error) {
	lifecycle, err := h.posts.UnpublishPost(ctx, PostLifecycleCommand{ID: req.Msg.Id})
	return render(lifecycle, err, LifecycleMessage)
}

func (h *ConnectHandler) ArchivePost(ctx context.Context, req *connect.Request[managev1.ArchivePostRequest]) (*connect.Response[managev1.PostLifecycleMutationResponse], error) {
	lifecycle, err := h.posts.ArchivePost(ctx, PostLifecycleCommand{ID: req.Msg.Id})
	return render(lifecycle, err, LifecycleMessage)
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
	lifecycle, err := h.posts.SchedulePost(ctx, command)
	return render(lifecycle, err, LifecycleMessage)
}

func (h *ConnectHandler) CancelPostSchedule(ctx context.Context, req *connect.Request[managev1.CancelPostScheduleRequest]) (*connect.Response[managev1.PostLifecycleMutationResponse], error) {
	lifecycle, err := h.posts.CancelPostSchedule(ctx, PostLifecycleCommand{ID: req.Msg.Id})
	return render(lifecycle, err, LifecycleMessage)
}

func (h *ConnectHandler) RepublishPost(ctx context.Context, req *connect.Request[managev1.RepublishPostRequest]) (*connect.Response[managev1.PostLifecycleMutationResponse], error) {
	lifecycle, err := h.posts.RepublishPost(ctx, PostLifecycleCommand{ID: req.Msg.Id})
	return render(lifecycle, err, LifecycleMessage)
}

func (h *ConnectHandler) ListPostsAdmin(ctx context.Context, req *connect.Request[managev1.ListPostsAdminRequest]) (*connect.Response[managev1.ListPostsAdminResponse], error) {
	listed, err := h.posts.ListPostsAdmin(ctx, ListPostsQuery{Page: page(req.Msg.GetPagination()), Filters: req.Msg.Filters, Sorts: req.Msg.Sorts})
	return render(listed, err, adminPageMessage)
}

func (h *ConnectHandler) ListMyPosts(ctx context.Context, req *connect.Request[managev1.ListMyPostsRequest]) (*connect.Response[managev1.ListMyPostsResponse], error) {
	listed, err := h.posts.ListMyPosts(ctx, ListPostsQuery{Page: page(req.Msg.GetPagination()), Filters: req.Msg.Filters, Sorts: req.Msg.Sorts})
	return render(listed, err, myPostsMessage)
}

func (h *ConnectHandler) SetPostFeaturedImage(ctx context.Context, req *connect.Request[managev1.SetPostFeaturedImageRequest]) (*connect.Response[managev1.SetPostFeaturedImageResponse], error) {
	featured, err := h.posts.SetPostFeaturedImage(ctx, SetPostFeaturedImageCommand{PostID: req.Msg.PostId, FileID: req.Msg.FileId})
	return render(featured, err, FeaturedImageMessage)
}

func (h *ConnectHandler) DeletePostFeaturedImage(ctx context.Context, req *connect.Request[managev1.DeletePostFeaturedImageRequest]) (*connect.Response[managev1.OgAssetDeleteResponse], error) {
	runID, err := h.posts.DeletePostFeaturedImage(ctx, DeletePostFeaturedImageCommand{PostID: req.Msg.PostId})
	return render(runID, err, FeaturedImageDeletedMessage)
}

func (h *ConnectHandler) ListPostParticipants(ctx context.Context, req *connect.Request[managev1.ListPostParticipantsRequest]) (*connect.Response[managev1.ListPostParticipantsResponse], error) {
	participants, err := h.posts.ListPostParticipants(ctx, PostParticipantsQuery{PostID: req.Msg.PostId})
	return render(participants, err, ParticipantsMessage)
}

func (h *ConnectHandler) AddPostAuthor(ctx context.Context, req *connect.Request[managev1.AddPostAuthorRequest]) (*connect.Response[managev1.PostParticipant], error) {
	participant, err := h.posts.AddPostAuthor(ctx, PostParticipantCommand{PostID: req.Msg.PostId, MemberID: req.Msg.MemberId})
	return render(participant, err, ParticipantMessage)
}

func (h *ConnectHandler) RemovePostAuthor(ctx context.Context, req *connect.Request[managev1.RemovePostAuthorRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return deleted(h.posts.RemovePostAuthor(ctx, PostParticipantCommand{PostID: req.Msg.PostId, MemberID: req.Msg.MemberId}))
}

func (h *ConnectHandler) AddPostCollaborator(ctx context.Context, req *connect.Request[managev1.AddPostCollaboratorRequest]) (*connect.Response[managev1.PostParticipant], error) {
	participant, err := h.posts.AddPostCollaborator(ctx, PostParticipantCommand{PostID: req.Msg.PostId, MemberID: req.Msg.MemberId})
	return render(participant, err, ParticipantMessage)
}

func (h *ConnectHandler) RemovePostCollaborator(ctx context.Context, req *connect.Request[managev1.RemovePostCollaboratorRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return deleted(h.posts.RemovePostCollaborator(ctx, PostParticipantCommand{PostID: req.Msg.PostId, MemberID: req.Msg.MemberId}))
}

func (h *ConnectHandler) ListPostVersions(ctx context.Context, req *connect.Request[managev1.ListPostVersionsRequest]) (*connect.Response[managev1.ListPostVersionsResponse], error) {
	versions, err := h.posts.ListPostVersions(ctx, ListPostVersionsQuery{PostID: req.Msg.PostId, Page: page(req.Msg.GetPagination())})
	return render(versions, err, VersionsMessage)
}

func (h *ConnectHandler) RestorePostVersion(ctx context.Context, req *connect.Request[managev1.RestorePostVersionRequest]) (*connect.Response[managev1.Post], error) {
	view, err := h.posts.RestorePostVersion(ctx, RestorePostVersionCommand{PostID: req.Msg.PostId, VersionID: req.Msg.VersionId})
	return render(view, err, PostMessage)
}

func (h *ConnectHandler) CheckSlugAvailable(ctx context.Context, req *connect.Request[managev1.CheckSlugAvailableRequest]) (*connect.Response[managev1.CheckSlugAvailableResponse], error) {
	available, err := h.posts.CheckSlugAvailable(ctx, CheckSlugAvailableQuery{Slug: req.Msg.Slug, ExcludePostID: req.Msg.ExcludePostId})
	return render(available, err, SlugAvailabilityMessage)
}

// InternalConnectHandler serves the Post collaboration RPCs editor-collab
// calls.
type InternalConnectHandler struct {
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
	room, err := h.posts.LoadPostBlockDocument(ctx, query)
	return render(room, err, roomDocumentMessage)
}

func (h *InternalConnectHandler) ApplyPostBlockBatch(ctx context.Context, req *connect.Request[intrav1.ApplyPostBlockBatchRequest]) (*connect.Response[intrav1.ApplyPostBlockBatchResponse], error) {
	msg := req.Msg
	saved, err := h.posts.ApplyPostBlockBatch(ctx, ApplyPostBlockBatchCommand{
		PostID: msg.PostId, Locale: msg.Locale, Batch: msg.Batch,
		AffectedLocaleValues: msg.AffectedLocaleValues, ExpectedTargetRevision: msg.ExpectedTargetRevision,
	})
	return render(saved, err, blockBatchMessage)
}

func (h *InternalConnectHandler) UpdatePostLocaleMetadata(ctx context.Context, req *connect.Request[intrav1.UpdatePostLocaleMetadataRequest]) (*connect.Response[intrav1.UpdatePostLocaleMetadataResponse], error) {
	msg := req.Msg
	saved, err := h.posts.UpdatePostLocaleMetadata(ctx, UpdatePostLocaleMetadataCommand{
		PostID: msg.PostId, Locale: msg.Locale, TitleChange: stringChange(msg.TitleChange), SummaryChange: stringChange(msg.SummaryChange),
		ExpectedRevision: msg.ExpectedRevision, ExpectedTargetRevision: msg.ExpectedTargetRevision,
		ContributorMemberIDs: msg.ContributorMemberIds,
	})
	return render(saved, err, localeMetadataMessage)
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
	saved, err := h.posts.UpdatePostDocumentMetadata(ctx, UpdatePostDocumentMetadataCommand{
		PostID: msg.PostId, Locale: msg.Locale, CategoryIDs: postStringIDs(msg.CategoryIds), TagIDs: postStringIDs(msg.TagIds),
		ExpectedRevision: msg.ExpectedRevision, ContributorMemberIDs: msg.ContributorMemberIds,
	})
	return render(saved, err, documentMetadataMessage)
}

func (h *InternalConnectHandler) CreatePostVersionCheckpoint(ctx context.Context, req *connect.Request[intrav1.CreatePostVersionCheckpointRequest]) (*connect.Response[intrav1.CreatePostVersionCheckpointResponse], error) {
	msg := req.Msg
	checkpoint, err := h.posts.CreatePostVersionCheckpoint(ctx, CreatePostVersionCheckpointCommand{
		PostID: msg.PostId, Locale: msg.Locale, ExpectedRevision: msg.ExpectedRevision, ContributorMemberIDs: msg.ContributorMemberIds,
	})
	return render(checkpoint, err, checkpointMessage)
}

// CommentConnectHandler serves the Post comment RPCs.
type CommentConnectHandler struct {
	comments *CommentService
}

// NewCommentConnectHandler returns the Connect adapter of comments.
func NewCommentConnectHandler(comments *CommentService) *CommentConnectHandler {
	return &CommentConnectHandler{comments: comments}
}

func (h *CommentConnectHandler) ListCommentsByPost(ctx context.Context, req *connect.Request[managev1.ListCommentsByPostRequest]) (*connect.Response[managev1.ListCommentsByPostResponse], error) {
	msg := req.Msg
	page, err := h.comments.ListCommentsByPost(ctx, ListCommentsQuery{PostID: msg.PostId, Limit: msg.Limit, Cursor: msg.Cursor, ReplyLimit: msg.ReplyLimit})
	return render(page, err, commentsMessage)
}

func (h *CommentConnectHandler) LoadMoreReplies(ctx context.Context, req *connect.Request[managev1.LoadMoreRepliesRequest]) (*connect.Response[managev1.LoadMoreRepliesResponse], error) {
	page, err := h.comments.LoadMoreReplies(ctx, LoadMoreRepliesQuery{CommentID: req.Msg.CommentId, Limit: req.Msg.Limit, Cursor: req.Msg.Cursor})
	return render(page, err, repliesMessage)
}

func (h *CommentConnectHandler) CreateComment(ctx context.Context, req *connect.Request[managev1.CreateCommentRequest]) (*connect.Response[managev1.CreateCommentResponse], error) {
	created, err := h.comments.CreateComment(ctx, CreateCommentCommand{PostID: req.Msg.PostId, ParentID: req.Msg.ParentId, Content: req.Msg.Content})
	return render(created, err, func(comment AuthoredComment) *managev1.CreateCommentResponse {
		return &managev1.CreateCommentResponse{Comment: commentWithAuthorMessage(comment)}
	})
}

func (h *CommentConnectHandler) UpdateComment(ctx context.Context, req *connect.Request[managev1.UpdateCommentRequest]) (*connect.Response[managev1.UpdateCommentResponse], error) {
	updated, err := h.comments.UpdateComment(ctx, UpdateCommentCommand{ID: req.Msg.Id, Content: req.Msg.Content})
	return render(updated, err, func(comment AuthoredComment) *managev1.UpdateCommentResponse {
		return &managev1.UpdateCommentResponse{Comment: commentWithAuthorMessage(comment)}
	})
}

func (h *CommentConnectHandler) DeleteComment(ctx context.Context, req *connect.Request[managev1.DeleteCommentRequest]) (*connect.Response[managev1.DeleteCommentResponse], error) {
	return respond(&managev1.DeleteCommentResponse{Success: true}, h.comments.DeleteComment(ctx, DeleteCommentCommand{ID: req.Msg.Id}))
}
