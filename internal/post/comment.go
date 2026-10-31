package post

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

const (
	maxCommentDepth     = 6
	maxContentLen       = 10000
	defaultCommentLimit = 20
	defaultReplyLimit   = 3
	maxCommentLimit     = 100
	maxReplyLimit       = 50
)

// Comment messages clients branch on.
const (
	msgCommentsDisabled         = "comments are disabled for this post"
	msgCannotCommentUnpublished = "cannot comment on unpublished post"
	msgCannotUpdateDeleted      = "cannot update deleted comment"
)

// CommentDeps are the collaborators of the comment use cases. Every field
// is required; NewCommentService rejects a missing one.
type CommentDeps struct {
	DB      *gorm.DB
	SpiceDB *auth.SpiceDBClient
	Members MemberSummaryLoader
	Audit   domainaudit.Appender
}

// CommentService holds the use cases of Post comments.
type CommentService struct {
	db        *gorm.DB
	spiceDB   *auth.SpiceDBClient
	members   MemberSummaryLoader
	mutations *mutation.Runner
}

// NewCommentService validates deps and returns the comment use cases.
func NewCommentService(deps CommentDeps) (*CommentService, error) {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": deps.DB == nil, "SpiceDB": deps.SpiceDB == nil, "Members": deps.Members == nil, "Audit": deps.Audit == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return nil, errors.New("comment dependencies are required: " + strings.Join(missing, ", "))
	}
	mutations, err := mutation.NewRunner(deps.DB, deps.SpiceDB, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &CommentService{db: deps.DB, spiceDB: deps.SpiceDB, members: deps.Members, mutations: mutations}, nil
}

// commentReader reads comments in a transaction that already authorized
// their Post.
type commentReader struct {
	db      *gorm.DB
	members MemberSummaryLoader
}

// ListCommentsQuery reads a page of a Post's top-level comments with their
// first replies.
type ListCommentsQuery struct {
	PostID     string
	Limit      int32
	Cursor     *string
	ReplyLimit int32
}

// LoadMoreRepliesQuery reads a page of a comment's direct replies.
type LoadMoreRepliesQuery struct {
	CommentID string
	Limit     int32
	Cursor    *string
}

// CreateCommentCommand comments on a published Post, under ParentID when
// set.
type CreateCommentCommand struct {
	PostID   string
	ParentID *string
	Content  string
}

type UpdateCommentCommand struct{ ID, Content string }
type DeleteCommentCommand struct{ ID string }

// ListCommentsByPost returns comments for a post with Reddit-style pagination
func (s *CommentService) ListCommentsByPost(ctx context.Context, query ListCommentsQuery) (_ *managev1.ListCommentsByPostResponse, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", query.PostID))

	// Require authentication for Secure API
	user := auth.GetUser(ctx)
	if user == nil {
		return nil, domainerr.AuthenticationRequired()
	}

	if query.PostID == "" {
		return nil, domainerr.Required("post_id")
	}

	var response *managev1.ListCommentsByPostResponse
	err := s.withReadablePost(ctx, query.PostID, func(tx *gorm.DB) error {
		var err error
		response, err = ReadComments(ctx, tx, s.members, query)
		return err
	})
	return response, err
}

// ReadComments reads within the caller's authorized Post snapshot tx.
// The caller must hold the Post root lock and check visibility and comments_enabled.
func ReadComments(ctx context.Context, tx *gorm.DB, members MemberSummaryLoader, query ListCommentsQuery) (*managev1.ListCommentsByPostResponse, error) {
	return commentReader{db: tx, members: members}.readComments(ctx, query)
}

func (s commentReader) readComments(ctx context.Context, request ListCommentsQuery) (*managev1.ListCommentsByPostResponse, error) {
	// Normalize pagination params
	limit := int(request.Limit)
	if limit <= 0 {
		limit = defaultCommentLimit
	}
	if limit > maxCommentLimit {
		limit = maxCommentLimit
	}

	replyLimit := int(request.ReplyLimit)
	if replyLimit <= 0 {
		replyLimit = defaultReplyLimit
	}
	if replyLimit > maxReplyLimit {
		replyLimit = maxReplyLimit
	}

	// Count total top-level comments
	var totalCount int64
	if err := s.db.WithContext(ctx).
		Model(&model.Comment{}).
		Where("post_id = ? AND parent_id IS NULL", request.PostID).
		Count(&totalCount).Error; err != nil {
		return nil, err
	}

	// Build query for top-level comments
	query := s.db.WithContext(ctx).
		Table("comment c").
		Select(`c.id, c.post_id, c.member_id, c.parent_id, c.content, c.is_deleted,
				c.created_at, c.updated_at`).
		Where("c.post_id = ? AND c.parent_id IS NULL", request.PostID).
		Order("c.created_at ASC, c.id ASC")

	query, err := s.applyCommentCursor(ctx, query, request.PostID, request.Cursor)
	if err != nil {
		return nil, err
	}

	// Fetch limit + 1 to know if there are more
	var topLevelComments []model.CommentWithAuthor
	if err := query.Limit(limit + 1).Find(&topLevelComments).Error; err != nil {
		return nil, err
	}

	// Determine if there are more comments
	hasMore := len(topLevelComments) > limit
	if hasMore {
		topLevelComments = topLevelComments[:limit]
	}

	// Get next cursor
	var nextCursor *string
	if hasMore && len(topLevelComments) > 0 {
		lastID := topLevelComments[len(topLevelComments)-1].ID
		nextCursor = &lastID
	}

	// Collect top-level comment IDs
	topLevelIDs := make([]string, len(topLevelComments))
	for i, c := range topLevelComments {
		topLevelIDs[i] = c.ID
	}

	// Fetch all replies for these top-level comments (we need all to build tree)
	var allReplies []model.CommentWithAuthor
	if len(topLevelIDs) > 0 {
		// Use recursive CTE to get all descendants
		err := s.db.WithContext(ctx).Raw(`
			WITH RECURSIVE comment_tree AS (
				SELECT id, parent_id, 1 as depth
				FROM comment
				WHERE post_id = ? AND parent_id IN (?)

				UNION ALL

				SELECT c.id, c.parent_id, ct.depth + 1
				FROM comment c
				INNER JOIN comment_tree ct ON c.parent_id = ct.id
				WHERE c.post_id = ? AND ct.depth < ?
			)
			SELECT c.id, c.post_id, c.member_id, c.parent_id, c.content, c.is_deleted,
				   c.created_at, c.updated_at
			FROM comment c
			WHERE c.id IN (SELECT id FROM comment_tree)
			ORDER BY c.created_at ASC, c.id ASC
		`, request.PostID, topLevelIDs, request.PostID, maxCommentDepth).Scan(&allReplies).Error
		if err != nil {
			return nil, err
		}
	}

	summaries, err := s.commentMemberSummaries(ctx, topLevelComments, allReplies)
	if err != nil {
		return nil, err
	}
	tree := buildCommentTreeWithPagination(topLevelComments, allReplies, replyLimit, summaries)

	return &managev1.ListCommentsByPostResponse{
		Comments:   tree,
		NextCursor: nextCursor,
		HasMore:    hasMore,
		TotalCount: int32(totalCount),
	}, nil
}

func (s commentReader) applyCommentCursor(
	ctx context.Context,
	query *gorm.DB,
	postID string,
	cursor *string,
) (*gorm.DB, error) {
	if cursor == nil || *cursor == "" {
		return query, nil
	}

	var cursorComment model.Comment
	err := s.db.WithContext(ctx).
		Select("created_at").
		Where("id = ? AND post_id = ? AND parent_id IS NULL", *cursor, postID).
		First(&cursorComment).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return query, nil
	}
	if err != nil {
		return nil, err
	}

	return query.Where(
		"c.created_at > ? OR (c.created_at = ? AND c.id > ?)",
		cursorComment.CreatedAt,
		cursorComment.CreatedAt,
		*cursor,
	), nil
}

// LoadMoreReplies returns more replies for a specific comment
func (s *CommentService) LoadMoreReplies(ctx context.Context, request LoadMoreRepliesQuery) (_ *managev1.LoadMoreRepliesResponse, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("comment_id", request.CommentID))

	// Require authentication for Secure API
	user := auth.GetUser(ctx)
	if user == nil {
		return nil, domainerr.AuthenticationRequired()
	}

	if request.CommentID == "" {
		return nil, domainerr.Required("comment_id")
	}

	// Find the parent comment and verify it exists
	var parentComment model.Comment
	if err := s.db.WithContext(ctx).Where("id = ?", request.CommentID).First(&parentComment).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.New(domainerr.NotFound, "", "comment not found")
		}
		return nil, err
	}

	var response *managev1.LoadMoreRepliesResponse
	err := s.withReadablePost(ctx, parentComment.PostID, func(tx *gorm.DB) error {
		var err error
		response, err = ReadCommentReplies(ctx, tx, s.members, request, parentComment)
		return err
	})
	return response, err
}

// ReadCommentReplies reads replies within the caller's authorized Post snapshot tx.
func ReadCommentReplies(ctx context.Context, tx *gorm.DB, members MemberSummaryLoader, query LoadMoreRepliesQuery, parentComment model.Comment) (*managev1.LoadMoreRepliesResponse, error) {
	return commentReader{db: tx, members: members}.readCommentReplies(ctx, query, parentComment)
}

func (s commentReader) readCommentReplies(ctx context.Context, request LoadMoreRepliesQuery, parentComment model.Comment) (*managev1.LoadMoreRepliesResponse, error) {
	// Normalize limit
	limit := int(request.Limit)
	if limit <= 0 {
		limit = 10
	}
	if limit > maxReplyLimit {
		limit = maxReplyLimit
	}

	// Count total direct replies
	var totalCount int64
	if err := s.db.WithContext(ctx).
		Model(&model.Comment{}).
		Where("post_id = ? AND parent_id = ?", parentComment.PostID, request.CommentID).
		Count(&totalCount).Error; err != nil {
		return nil, err
	}

	// Build query for direct replies
	query := s.db.WithContext(ctx).
		Table("comment c").
		Select(`c.id, c.post_id, c.member_id, c.parent_id, c.content, c.is_deleted,
				c.created_at, c.updated_at`).
		Where("c.post_id = ? AND c.parent_id = ?", parentComment.PostID, request.CommentID).
		Order("c.created_at ASC, c.id ASC")

	// Apply cursor if provided
	if request.Cursor != nil && *request.Cursor != "" {
		var cursorComment model.Comment
		if err := s.db.WithContext(ctx).
			Select("created_at").
			Where("id = ? AND post_id = ? AND parent_id = ?", *request.Cursor, parentComment.PostID, request.CommentID).
			First(&cursorComment).Error; err == nil {
			query = query.Where("c.created_at > ? OR (c.created_at = ? AND c.id > ?)",
				cursorComment.CreatedAt, cursorComment.CreatedAt, *request.Cursor)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			slog.ErrorContext(ctx, "Reply cursor lookup failed; listing replies from the start", "domain", "post", "operation", "load_reply_cursor", "outcome", "failed", failurecause.Attr(err), "comment_id", request.CommentID, "cursor_id", *request.Cursor)
		}
	}

	// Fetch limit + 1 to know if there are more
	var replies []model.CommentWithAuthor
	if err := query.Limit(limit + 1).Find(&replies).Error; err != nil {
		return nil, err
	}

	hasMore := len(replies) > limit
	if hasMore {
		replies = replies[:limit]
	}

	var nextCursor *string
	if hasMore && len(replies) > 0 {
		lastID := replies[len(replies)-1].ID
		nextCursor = &lastID
	}

	// For each reply, we need to get their nested replies too (simplified: just direct children)
	replyIDs := make([]string, len(replies))
	for i, r := range replies {
		replyIDs[i] = r.ID
	}

	var nestedReplies []model.CommentWithAuthor
	if len(replyIDs) > 0 {
		// Get all descendants
		if err := s.db.WithContext(ctx).Raw(`
			WITH RECURSIVE comment_tree AS (
				SELECT id, parent_id, 1 as depth
				FROM comment
				WHERE post_id = ? AND parent_id IN (?)

				UNION ALL

				SELECT c.id, c.parent_id, ct.depth + 1
				FROM comment c
				INNER JOIN comment_tree ct ON c.parent_id = ct.id
				WHERE c.post_id = ? AND ct.depth < ?
			)
			SELECT c.id, c.post_id, c.member_id, c.parent_id, c.content, c.is_deleted,
				   c.created_at, c.updated_at
			FROM comment c
			WHERE c.id IN (SELECT id FROM comment_tree)
			ORDER BY c.created_at ASC, c.id ASC
		`, parentComment.PostID, replyIDs, parentComment.PostID, maxCommentDepth).Scan(&nestedReplies).Error; err != nil {
			return nil, err
		}
	}

	summaries, err := s.commentMemberSummaries(ctx, replies, nestedReplies)
	if err != nil {
		return nil, err
	}
	tree := buildCommentTreeWithPagination(replies, nestedReplies, defaultReplyLimit, summaries)

	return &managev1.LoadMoreRepliesResponse{
		Replies:    tree,
		NextCursor: nextCursor,
		HasMore:    hasMore,
		TotalCount: int32(totalCount),
	}, nil
}

// commentMutation runs change, which a comment's author or a Post manager
// may make; the comment is read back for the response.
func (s *CommentService) commentMutation(ctx context.Context, access mutation.Access, change func(context.Context, *gorm.DB) (model.Comment, error)) (model.Comment, error) {
	return mutation.Run(ctx, s.mutations, mutation.Spec[model.Comment]{
		Access: access,
		Change: change,
		Audit:  func(model.Comment) mutation.Audit { return mutation.Unchanged() },
	})
}

// CreateComment creates a new comment
func (s *CommentService) CreateComment(ctx context.Context, command CreateCommentCommand) (*managev1.CreateCommentResponse, error) {
	user := auth.GetUser(ctx)
	if user == nil || user.MemberID.String() == "" {
		return nil, domainerr.AuthenticationRequired()
	}
	if err := validateCreateCommentCommand(command); err != nil {
		return nil, err
	}
	access := mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
		if err := lockCommentablePost(ctx, tx, command.PostID); err != nil {
			return err
		}
		return requireActiveCommentPrincipal(ctx, tx, user)
	})
	comment, err := s.commentMutation(ctx, access, func(ctx context.Context, tx *gorm.DB) (model.Comment, error) {
		return createCommentWithDB(ctx, tx, user, command)
	})
	if err != nil {
		return nil, err
	}
	reader := commentReader{db: s.db, members: s.members}
	result, err := reader.loadCommentWithAuthor(ctx, comment.ID)
	if err != nil {
		return nil, err
	}
	summaries, err := reader.commentMemberSummaries(ctx, []model.CommentWithAuthor{result}, nil)
	if err != nil {
		return nil, err
	}
	return &managev1.CreateCommentResponse{Comment: toProtoCommentWithAuthor(&result, summaries)}, nil
}

func validateCreateCommentCommand(command CreateCommentCommand) error {
	if command.PostID == "" {
		return domainerr.Required("post_id")
	}
	if command.Content == "" {
		return domainerr.Required("content")
	}
	if len(command.Content) > maxContentLen {
		return domainerr.InvalidField("content", fmt.Sprintf("exceeds maximum length of %d characters", maxContentLen))
	}
	return nil
}

func createCommentWithDB(
	ctx context.Context,
	tx *gorm.DB,
	user *auth.UserInfo,
	command CreateCommentCommand,
) (model.Comment, error) {
	parentID, err := resolveCommentParentWithDB(ctx, tx, command.PostID, command.ParentID)
	if err != nil {
		return model.Comment{}, err
	}
	memberID := user.MemberID.String()
	comment := model.Comment{
		PostID: command.PostID, MemberID: &memberID, ParentID: parentID, Content: command.Content,
	}
	if err := tx.Create(&comment).Error; err != nil {
		return model.Comment{}, err
	}
	return comment, nil
}

func lockCommentablePost(ctx context.Context, tx *gorm.DB, postID string) error {
	var post model.Post
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id, status, comments_enabled").Where("id = ?", postID).First(&post).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domainerr.New(domainerr.NotFound, "", "post not found")
		}
		return err
	}
	if !post.CommentsEnabled {
		return domainerr.Precondition(msgCommentsDisabled)
	}
	if post.Status != model.PostStatusPublished {
		return domainerr.Precondition(msgCannotCommentUnpublished)
	}
	return nil
}

func requireActiveCommentPrincipal(ctx context.Context, tx *gorm.DB, user *auth.UserInfo) error {
	active, err := identitystate.LockActivePrincipal(ctx, tx, user)
	if err != nil {
		return fmt.Errorf("lock comment author: %w", err)
	}
	if !active {
		return domainerr.Denied("comment authority was revoked")
	}
	return nil
}

func resolveCommentParentWithDB(
	ctx context.Context,
	tx *gorm.DB,
	postID string,
	requestedParentID *string,
) (*string, error) {
	if requestedParentID == nil || *requestedParentID == "" {
		return nil, nil
	}
	var parent model.Comment
	if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ? AND post_id = ?", *requestedParentID, postID).First(&parent).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.New(domainerr.NotFound, "", "parent comment not found")
		}
		return nil, err
	}
	depth, err := getCommentDepthTx(tx, ctx, parent.ID, postID)
	if err != nil {
		return nil, err
	}
	if depth >= maxCommentDepth {
		return nil, domainerr.Precondition(fmt.Sprintf("maximum comment depth of %d reached", maxCommentDepth))
	}
	return &parent.ID, nil
}

func (s commentReader) loadCommentWithAuthor(ctx context.Context, commentID string) (model.CommentWithAuthor, error) {
	var comment model.CommentWithAuthor
	err := s.db.WithContext(ctx).Table("comment c").
		Select(`c.id, c.post_id, c.member_id, c.parent_id, c.content, c.is_deleted,
			c.created_at, c.updated_at`).
		Where("c.id = ?", commentID).First(&comment).Error
	return comment, err
}

// UpdateComment updates a comment's content
func (s *CommentService) UpdateComment(ctx context.Context, command UpdateCommentCommand) (_ *managev1.UpdateCommentResponse, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("comment_id", command.ID))

	user := auth.GetUser(ctx)
	if user == nil || user.MemberID.String() == "" {
		return nil, domainerr.AuthenticationRequired()
	}
	memberID := user.MemberID.String()
	if command.ID == "" {
		return nil, domainerr.Required("id")
	}
	if command.Content == "" {
		return nil, domainerr.Required("content")
	}
	if len(command.Content) > maxContentLen {
		return nil, domainerr.InvalidField("content", fmt.Sprintf("exceeds maximum length of %d characters", maxContentLen))
	}
	var result model.CommentWithAuthor
	_, err := s.commentMutation(ctx, mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
		return requireActiveCommentPrincipal(ctx, tx, user)
	}), func(ctx context.Context, tx *gorm.DB) (model.Comment, error) {
		comment, err := lockComment(ctx, tx, command.ID)
		if err != nil {
			return comment, err
		}
		if comment.IsDeleted {
			return comment, domainerr.Precondition(msgCannotUpdateDeleted)
		}
		if comment.MemberID == nil || *comment.MemberID != memberID {
			return comment, domainerr.Denied("permission denied")
		}
		if err := tx.Model(&comment).Update("content", command.Content).Error; err != nil {
			return comment, err
		}
		result, err = commentReader{db: tx}.loadCommentWithAuthor(ctx, comment.ID)
		return comment, err
	})
	if err != nil {
		return nil, err
	}
	summaries, err := commentReader{db: s.db, members: s.members}.commentMemberSummaries(ctx, []model.CommentWithAuthor{result}, nil)
	if err != nil {
		return nil, err
	}
	return &managev1.UpdateCommentResponse{Comment: toProtoCommentWithAuthor(&result, summaries)}, nil
}

func lockComment(ctx context.Context, tx *gorm.DB, commentID string) (model.Comment, error) {
	var comment model.Comment
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", commentID).
		First(&comment).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return comment, domainerr.New(domainerr.NotFound, "", "comment not found")
		}
		return comment, err
	}
	return comment, nil
}

// DeleteComment soft-deletes a comment
func (s *CommentService) DeleteComment(ctx context.Context, command DeleteCommentCommand) (returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("comment_id", command.ID))

	user := auth.GetUser(ctx)
	if user == nil || user.MemberID.String() == "" {
		return domainerr.AuthenticationRequired()
	}
	memberID := user.MemberID.String()
	if command.ID == "" {
		return domainerr.Required("id")
	}
	var target struct {
		PostID string `gorm:"column:post_id"`
	}
	if err := s.db.WithContext(ctx).Table("comment").Select("post_id").Where("id = ?", command.ID).Take(&target).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domainerr.New(domainerr.NotFound, "", "comment not found")
		}
		return err
	}
	var comment model.Comment
	// The author deletes their own comment; anyone else needs to manage the
	// Post.
	access := mutation.Locked(func(ctx context.Context, tx *gorm.DB, require mutation.Require) error {
		postStatus, err := lockPostParticipantRoot(ctx, tx, target.PostID)
		if err != nil {
			return err
		}
		active, err := identitystate.LockActivePrincipal(ctx, tx, user)
		if err != nil {
			return fmt.Errorf("lock comment deletion principal: %w", err)
		}
		if !active {
			return domainerr.Denied("comment authority was revoked")
		}
		if comment, err = lockComment(ctx, tx, command.ID); err != nil {
			return err
		}
		if comment.MemberID != nil && *comment.MemberID == memberID {
			return nil
		}
		_, err = requireLockedPostActionForStatus(require, comment.PostID, postStatus, policyv1.Post.Manage)
		return err
	})
	_, err := s.commentMutation(ctx, access, func(_ context.Context, tx *gorm.DB) (model.Comment, error) {
		return comment, tx.Model(&comment).Updates(structured.Fields{
			"is_deleted": true,
			"content":    "[This comment has been deleted]",
		}).Error
	})
	return err
}

// withReadablePost runs read in one transaction after locking the Post and
// checking the caller may read its comments.
func (s *CommentService) withReadablePost(ctx context.Context, postID string, read func(*gorm.DB) error) error {
	access := mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
		var post model.Post
		if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).Select("id, status, comments_enabled").First(&post, "id = ?", postID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domainerr.New(domainerr.NotFound, "", "post not found")
			}
			return err
		}
		return s.requireCommentPostView(ctx, post)
	})
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[struct{}]{
		Access: access,
		Change: func(_ context.Context, tx *gorm.DB) (struct{}, error) { return struct{}{}, read(tx) },
		Audit:  func(struct{}) mutation.Audit { return mutation.Unchanged() },
	})
	return err
}

func (s *CommentService) requireCommentPostView(ctx context.Context, post model.Post) error {
	if post.Status != model.PostStatusPublished &&
		post.Status != model.PostStatusArchived {
		if _, err := requirePostViewForStatus(ctx, s.spiceDB, post.ID, post.Status); err != nil {
			return err
		}
	}
	if !post.CommentsEnabled {
		return domainerr.Precondition(msgCommentsDisabled)
	}
	return nil
}

// Helper to get comment depth using recursive CTE within a transaction
func getCommentDepthTx(tx *gorm.DB, ctx context.Context, commentID string, postID string) (int, error) {
	var depth int
	query := `
		WITH RECURSIVE comment_tree AS (
			SELECT id, parent_id, 1 as depth
			FROM comment
			WHERE id = ? AND post_id = ?

			UNION ALL

			SELECT c.id, c.parent_id, ct.depth + 1
			FROM comment c
			INNER JOIN comment_tree ct ON c.id = ct.parent_id
			WHERE c.post_id = ? AND ct.depth < ?
		)
		SELECT COALESCE(MAX(depth), 0) FROM comment_tree
	`
	if err := tx.WithContext(ctx).Raw(query, commentID, postID, postID, maxCommentDepth).Scan(&depth).Error; err != nil {
		return 0, err
	}
	return depth, nil
}

// Helper to build tree structure with reply pagination (Reddit-style)
func buildCommentTreeWithPagination(
	topLevel []model.CommentWithAuthor,
	allReplies []model.CommentWithAuthor,
	replyLimit int,
	summaries map[string]*commonv1.MemberSummary,
) []*managev1.CommentNode {
	// Create map for all comments
	nodeMap := make(map[string]*managev1.CommentNode)
	replyCountMap := make(map[string]int) // Direct reply count per parent

	// First pass: create nodes for top-level comments
	for i := range topLevel {
		nodeMap[topLevel[i].ID] = toProtoCommentNode(&topLevel[i], summaries)
	}

	// Second pass: create nodes for all replies and count direct children
	for i := range allReplies {
		nodeMap[allReplies[i].ID] = toProtoCommentNode(&allReplies[i], summaries)
		if allReplies[i].ParentID != nil && *allReplies[i].ParentID != "" {
			replyCountMap[*allReplies[i].ParentID]++
		}
	}

	// Third pass: build tree with pagination
	// Track how many direct children we've added per parent
	addedCount := make(map[string]int)

	for i := range allReplies {
		reply := &allReplies[i]
		node := nodeMap[reply.ID]

		if reply.ParentID == nil || *reply.ParentID == "" {
			continue
		}

		parent, ok := nodeMap[*reply.ParentID]
		if !ok {
			continue
		}

		// Only add up to replyLimit direct children
		if addedCount[*reply.ParentID] < replyLimit {
			parent.Replies = append(parent.Replies, node)
			addedCount[*reply.ParentID]++
		}
	}

	// Fourth pass: set has_more_replies and total_reply_count for all nodes
	for id, node := range nodeMap {
		totalReplies := replyCountMap[id]
		node.TotalReplyCount = int32(totalReplies)
		node.HasMoreReplies = len(node.Replies) < totalReplies
	}

	// Build result slice (only top-level comments)
	result := make([]*managev1.CommentNode, len(topLevel))
	for i := range topLevel {
		result[i] = nodeMap[topLevel[i].ID]
	}

	return result
}

// Helper to convert model to proto CommentNode
func toProtoCommentNode(c *model.CommentWithAuthor, summaries map[string]*commonv1.MemberSummary) *managev1.CommentNode {
	node := &managev1.CommentNode{
		Id:        c.ID,
		PostId:    c.PostID,
		Content:   c.Content,
		IsDeleted: c.IsDeleted,
		CreatedAt: timestamppb.New(c.CreatedAt),
		UpdatedAt: timestamppb.New(c.UpdatedAt),
		Replies:   []*managev1.CommentNode{},
	}

	if c.MemberID != nil {
		node.MemberId = c.MemberID
		node.Author = summaries[*c.MemberID]
	}
	if c.ParentID != nil {
		node.ParentId = c.ParentID
	}
	return node
}

// Helper to convert model to proto CommentWithAuthor
func toProtoCommentWithAuthor(c *model.CommentWithAuthor, summaries map[string]*commonv1.MemberSummary) *managev1.CommentWithAuthor {
	comment := &managev1.CommentWithAuthor{
		Id:        c.ID,
		PostId:    c.PostID,
		Content:   c.Content,
		IsDeleted: c.IsDeleted,
		CreatedAt: timestamppb.New(c.CreatedAt),
		UpdatedAt: timestamppb.New(c.UpdatedAt),
	}

	if c.MemberID != nil {
		comment.MemberId = c.MemberID
		comment.Author = summaries[*c.MemberID]
	}
	if c.ParentID != nil {
		comment.ParentId = c.ParentID
	}
	return comment
}

func (s commentReader) commentMemberSummaries(
	ctx context.Context,
	groups ...[]model.CommentWithAuthor,
) (map[string]*commonv1.MemberSummary, error) {
	seen := map[string]struct{}{}
	memberIDs := make([]string, 0)
	for _, comments := range groups {
		for i := range comments {
			if comments[i].MemberID == nil || *comments[i].MemberID == "" {
				continue
			}
			if _, exists := seen[*comments[i].MemberID]; exists {
				continue
			}
			seen[*comments[i].MemberID] = struct{}{}
			memberIDs = append(memberIDs, *comments[i].MemberID)
		}
	}
	return s.members.LoadMemberSummaries(ctx, memberIDs)
}
