package public

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

type commentMembers struct{ MemberSummaryLoader }

func (m commentMembers) LoadMemberSummaries(ctx context.Context, ids []string) (map[string]*commonv1.MemberSummary, error) {
	return m.LoadPublicMemberSummaries(ctx, ids)
}

func (s *PostService) withCommentPost(
	ctx context.Context,
	postID, shareToken, sharePassword string,
	read func(*gorm.DB) error,
) error {
	if !isValidUUID(postID) {
		return errs.InvalidArgument("post_id", "must be a canonical UUID")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var post model.Post
		if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
			Select("id, status, comments_enabled").First(&post, "id = ?", postID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.NotFoundMsg("post not found")
			}
			return errs.Internal(err)
		}
		if !isPublicPostStatus(post.Status) {
			if _, _, err := s.requireDraftPostAccess(ctx, tx, post.ID, shareToken, sharePassword); err != nil {
				return err
			}
		}
		if !post.CommentsEnabled {
			return errs.FailedPrecondition(errs.MsgCommentsDisabled)
		}
		return read(tx)
	})
}

func (s *PostService) ListComments(
	ctx context.Context,
	req *connect.Request[openv1.ListPostCommentsRequest],
) (*connect.Response[openv1.ListPostCommentsResponse], error) {
	var response *openv1.ListPostCommentsResponse
	err := s.withCommentPost(ctx, req.Msg.PostId, req.Msg.GetShareToken(), req.Msg.GetSharePassword(), func(tx *gorm.DB) error {
		reader := postdomain.NewCommentService(tx, s.spiceDB, commentMembers{s.members})
		result, err := reader.ReadCommentsByPost(ctx, connect.NewRequest(&managev1.ListCommentsByPostRequest{
			PostId: req.Msg.PostId, Limit: req.Msg.Limit, Cursor: req.Msg.Cursor, ReplyLimit: req.Msg.ReplyLimit,
		}))
		if err != nil {
			return err
		}
		response = &openv1.ListPostCommentsResponse{
			Comments: publicCommentNodes(result.Msg.Comments), NextCursor: result.Msg.NextCursor,
			HasMore: result.Msg.HasMore, TotalCount: result.Msg.TotalCount,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(response), nil
}

func (s *PostService) LoadMoreCommentReplies(
	ctx context.Context,
	req *connect.Request[openv1.LoadMorePostCommentRepliesRequest],
) (*connect.Response[openv1.ListPostCommentsResponse], error) {
	if !isValidUUID(req.Msg.CommentId) {
		return nil, errs.InvalidArgument("comment_id", "must be a canonical UUID")
	}
	var response *openv1.ListPostCommentsResponse
	err := s.withCommentPost(ctx, req.Msg.PostId, req.Msg.GetShareToken(), req.Msg.GetSharePassword(), func(tx *gorm.DB) error {
		var parent model.Comment
		if err := tx.First(&parent, "id = ? AND post_id = ?", req.Msg.CommentId, req.Msg.PostId).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.NotFoundMsg("comment not found")
			}
			return errs.Internal(err)
		}
		reader := postdomain.NewCommentService(tx, s.spiceDB, commentMembers{s.members})
		result, err := reader.ReadCommentReplies(ctx, connect.NewRequest(&managev1.LoadMoreRepliesRequest{
			CommentId: req.Msg.CommentId, Limit: req.Msg.Limit, Cursor: req.Msg.Cursor,
		}), parent)
		if err != nil {
			return err
		}
		response = &openv1.ListPostCommentsResponse{
			Comments: publicCommentNodes(result.Msg.Replies), NextCursor: result.Msg.NextCursor,
			HasMore: result.Msg.HasMore, TotalCount: result.Msg.TotalCount,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(response), nil
}

func publicCommentNodes(nodes []*managev1.CommentNode) []*openv1.PostCommentNode {
	result := make([]*openv1.PostCommentNode, len(nodes))
	for index, node := range nodes {
		result[index] = &openv1.PostCommentNode{
			Id: node.Id, PostId: node.PostId, ParentId: node.ParentId, Content: node.Content,
			IsDeleted: node.IsDeleted, CreatedAt: node.CreatedAt, UpdatedAt: node.UpdatedAt,
			Replies: publicCommentNodes(node.Replies), HasMoreReplies: node.HasMoreReplies,
			TotalReplyCount: node.TotalReplyCount, MemberId: node.MemberId, Author: node.Author,
		}
	}
	return result
}
