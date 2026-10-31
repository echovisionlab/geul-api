package public

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

type commentMembers struct{ MemberSummaryLoader }

func (m commentMembers) LoadMemberSummaries(ctx context.Context, ids []string) (map[string]*commonv1.MemberSummary, error) {
	return m.LoadPublicMemberSummaries(ctx, ids)
}

// ListPostCommentsQuery reads a page of a public Post's comments. A draft
// Post's comments are visible through its share link.
type ListPostCommentsQuery struct {
	PostID        string
	Limit         int32
	Cursor        *string
	ReplyLimit    int32
	ShareToken    string
	SharePassword string
}

// LoadMorePostCommentRepliesQuery reads a page of a public comment's replies.
type LoadMorePostCommentRepliesQuery struct {
	PostID        string
	CommentID     string
	Limit         int32
	Cursor        *string
	ShareToken    string
	SharePassword string
}

func (s *Service) withCommentPost(
	ctx context.Context,
	postID, shareToken, sharePassword string,
	read func(*gorm.DB) error,
) error {
	if !isValidUUID(postID) {
		return domainerr.InvalidField("post_id", "must be a canonical UUID")
	}
	return mutation.ReadLocked(ctx, s.db, func(tx *gorm.DB) error {
		var post model.Post
		if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
			Select("id, status, comments_enabled").First(&post, "id = ?", postID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domainerr.New(domainerr.NotFound, "", "post not found")
			}
			return err
		}
		if !isPublicPostStatus(post.Status) {
			if _, _, err := s.requireDraftPostAccess(ctx, tx, post.ID, shareToken, sharePassword); err != nil {
				return err
			}
		}
		if !post.CommentsEnabled {
			return domainerr.Precondition("comments are disabled for this post")
		}
		return read(tx)
	})
}

// ListComments reads a page of a public Post's comments.
func (s *Service) ListComments(ctx context.Context, query ListPostCommentsQuery) (*openv1.ListPostCommentsResponse, error) {
	var response *openv1.ListPostCommentsResponse
	err := s.withCommentPost(ctx, query.PostID, query.ShareToken, query.SharePassword, func(tx *gorm.DB) error {
		result, err := postdomain.ReadComments(ctx, tx, commentMembers{s.members}, postdomain.ListCommentsQuery{
			PostID: query.PostID, Limit: query.Limit, Cursor: query.Cursor, ReplyLimit: query.ReplyLimit,
		})
		if err != nil {
			return err
		}
		response = &openv1.ListPostCommentsResponse{
			Comments: publicCommentNodes(result.Comments), NextCursor: result.NextCursor,
			HasMore: result.HasMore, TotalCount: result.TotalCount,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return response, nil
}

// LoadMoreCommentReplies reads a page of a public comment's replies.
func (s *Service) LoadMoreCommentReplies(ctx context.Context, query LoadMorePostCommentRepliesQuery) (*openv1.ListPostCommentsResponse, error) {
	if !isValidUUID(query.CommentID) {
		return nil, domainerr.InvalidField("comment_id", "must be a canonical UUID")
	}
	var response *openv1.ListPostCommentsResponse
	err := s.withCommentPost(ctx, query.PostID, query.ShareToken, query.SharePassword, func(tx *gorm.DB) error {
		var parent model.Comment
		if err := tx.First(&parent, "id = ? AND post_id = ?", query.CommentID, query.PostID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domainerr.New(domainerr.NotFound, "", "comment not found")
			}
			return err
		}
		result, err := postdomain.ReadCommentReplies(ctx, tx, commentMembers{s.members}, postdomain.LoadMoreRepliesQuery{
			CommentID: query.CommentID, Limit: query.Limit, Cursor: query.Cursor,
		}, parent)
		if err != nil {
			return err
		}
		response = &openv1.ListPostCommentsResponse{
			Comments: publicCommentNodes(result.Replies), NextCursor: result.NextCursor,
			HasMore: result.HasMore, TotalCount: result.TotalCount,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return response, nil
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
