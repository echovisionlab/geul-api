package public

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/post"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
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
	read func(Store) error,
) error {
	if !isValidUUID(postID) {
		return domainerr.InvalidField("post_id", "must be a canonical UUID")
	}
	return s.units.ReadLocked(ctx, func(store Store) error {
		commented, err := store.ShareLockCommentPost(ctx, postID)
		if err != nil {
			if errors.Is(err, post.ErrPostNotFound) {
				return domainerr.New(domainerr.NotFound, "", "post not found")
			}
			return err
		}
		if !isPublicPostStatus(commented.Status) {
			if _, _, err := s.requireDraftPostAccess(ctx, store, commented.ID, shareToken, sharePassword); err != nil {
				return err
			}
		}
		if !commented.CommentsEnabled {
			return domainerr.Precondition("comments are disabled for this post")
		}
		return read(store)
	})
}

// ListComments reads a page of a public Post's comments.
func (s *Service) ListComments(ctx context.Context, query ListPostCommentsQuery) (post.CommentPage, error) {
	var page post.CommentPage
	err := s.withCommentPost(ctx, query.PostID, query.ShareToken, query.SharePassword, func(store Store) error {
		var err error
		page, err = post.ReadComments(ctx, store, commentMembers{s.members}, post.ListCommentsQuery{
			PostID: query.PostID, Limit: query.Limit, Cursor: query.Cursor, ReplyLimit: query.ReplyLimit,
		})
		return err
	})
	if err != nil {
		return post.CommentPage{}, err
	}
	return page, nil
}

// LoadMoreCommentReplies reads a page of a public comment's replies.
func (s *Service) LoadMoreCommentReplies(ctx context.Context, query LoadMorePostCommentRepliesQuery) (post.CommentPage, error) {
	if !isValidUUID(query.CommentID) {
		return post.CommentPage{}, domainerr.InvalidField("comment_id", "must be a canonical UUID")
	}
	var page post.CommentPage
	err := s.withCommentPost(ctx, query.PostID, query.ShareToken, query.SharePassword, func(store Store) error {
		parent, err := store.FindPostComment(ctx, query.PostID, query.CommentID)
		if err != nil {
			if errors.Is(err, post.ErrCommentNotFound) {
				return domainerr.New(domainerr.NotFound, "", "comment not found")
			}
			return err
		}
		page, err = post.ReadCommentReplies(ctx, store, commentMembers{s.members}, post.LoadMoreRepliesQuery{
			CommentID: query.CommentID, Limit: query.Limit, Cursor: query.Cursor,
		}, parent)
		return err
	})
	if err != nil {
		return post.CommentPage{}, err
	}
	return page, nil
}
