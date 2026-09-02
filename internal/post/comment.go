package post

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
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

type CommentService struct {
	db      *gorm.DB
	spiceDB *auth.SpiceDBClient
	members MemberSummaryLoader
}

func NewCommentService(db *gorm.DB, spiceDB *auth.SpiceDBClient, members MemberSummaryLoader) *CommentService {
	if db == nil || members == nil {
		panic("comment service dependencies are required")
	}
	return &CommentService{
		db:      db,
		spiceDB: spiceDB,
		members: members,
	}
}

// ListCommentsByPost returns comments for a post with Reddit-style pagination
func (s *CommentService) ListCommentsByPost(
	ctx context.Context,
	req *connect.Request[managev1.ListCommentsByPostRequest],
) (*connect.Response[managev1.ListCommentsByPostResponse], error) {
	// Require authentication for Secure API
	user := auth.GetUser(ctx)
	if user == nil {
		return nil, errs.AuthenticationRequired()
	}

	if req.Msg.PostId == "" {
		return nil, errs.Required("post_id")
	}

	// Check if post exists and get its status
	var post model.Post
	if err := s.db.WithContext(ctx).Select("id, status").Where("id = ?", req.Msg.PostId).First(&post).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFoundMsg("post not found")
		}
		return nil, errs.Internal(err)
	}

	if err := requirePublicCommentPost(post); err != nil {
		return nil, err
	}

	// Normalize pagination params
	limit := int(req.Msg.Limit)
	if limit <= 0 {
		limit = defaultCommentLimit
	}
	if limit > maxCommentLimit {
		limit = maxCommentLimit
	}

	replyLimit := int(req.Msg.ReplyLimit)
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
		Where("post_id = ? AND parent_id IS NULL", req.Msg.PostId).
		Count(&totalCount).Error; err != nil {
		return nil, errs.Internal(err)
	}

	// Build query for top-level comments
	query := s.db.WithContext(ctx).
		Table("comment c").
		Select(`c.id, c.post_id, c.member_id, c.parent_id, c.content, c.is_deleted,
				c.created_at, c.updated_at`).
		Where("c.post_id = ? AND c.parent_id IS NULL", req.Msg.PostId).
		Order("c.created_at ASC, c.id ASC")

	query, err := s.applyCommentCursor(ctx, query, req.Msg.PostId, req.Msg.Cursor)
	if err != nil {
		return nil, err
	}

	// Fetch limit + 1 to know if there are more
	var topLevelComments []model.CommentWithAuthor
	if err := query.Limit(limit + 1).Find(&topLevelComments).Error; err != nil {
		return nil, errs.Internal(err)
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
		`, req.Msg.PostId, topLevelIDs, req.Msg.PostId, maxCommentDepth).Scan(&allReplies).Error
		if err != nil {
			return nil, errs.Internal(err)
		}
	}

	summaries, err := s.commentMemberSummaries(ctx, topLevelComments, allReplies)
	if err != nil {
		return nil, errs.Internal(err)
	}
	tree := s.buildCommentTreeWithPagination(topLevelComments, allReplies, replyLimit, summaries)

	return connect.NewResponse(&managev1.ListCommentsByPostResponse{
		Comments:   tree,
		NextCursor: nextCursor,
		HasMore:    hasMore,
		TotalCount: int32(totalCount),
	}), nil
}

func (s *CommentService) applyCommentCursor(
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
	if err == gorm.ErrRecordNotFound {
		return query, nil
	}
	if err != nil {
		return nil, errs.Internal(err)
	}

	return query.Where(
		"c.created_at > ? OR (c.created_at = ? AND c.id > ?)",
		cursorComment.CreatedAt,
		cursorComment.CreatedAt,
		*cursor,
	), nil
}

// LoadMoreReplies returns more replies for a specific comment
func (s *CommentService) LoadMoreReplies(
	ctx context.Context,
	req *connect.Request[managev1.LoadMoreRepliesRequest],
) (*connect.Response[managev1.LoadMoreRepliesResponse], error) {
	// Require authentication for Secure API
	user := auth.GetUser(ctx)
	if user == nil {
		return nil, errs.AuthenticationRequired()
	}

	if req.Msg.CommentId == "" {
		return nil, errs.Required("comment_id")
	}

	// Find the parent comment and verify it exists
	var parentComment model.Comment
	if err := s.db.WithContext(ctx).Where("id = ?", req.Msg.CommentId).First(&parentComment).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFoundMsg("comment not found")
		}
		return nil, errs.Internal(err)
	}

	// Check if post is accessible
	var post model.Post
	if err := s.db.WithContext(ctx).Select("id, status").Where("id = ?", parentComment.PostID).First(&post).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFoundMsg("post not found")
		}
		return nil, errs.Internal(err)
	}

	if err := requirePublicCommentPost(post); err != nil {
		if connect.CodeOf(err) == connect.CodeNotFound {
			return nil, errs.NotFoundMsg("comment not found")
		}
		return nil, err
	}

	// Normalize limit
	limit := int(req.Msg.Limit)
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
		Where("post_id = ? AND parent_id = ?", parentComment.PostID, req.Msg.CommentId).
		Count(&totalCount).Error; err != nil {
		return nil, errs.Internal(err)
	}

	// Build query for direct replies
	query := s.db.WithContext(ctx).
		Table("comment c").
		Select(`c.id, c.post_id, c.member_id, c.parent_id, c.content, c.is_deleted,
				c.created_at, c.updated_at`).
		Where("c.post_id = ? AND c.parent_id = ?", parentComment.PostID, req.Msg.CommentId).
		Order("c.created_at ASC, c.id ASC")

	// Apply cursor if provided
	if req.Msg.Cursor != nil && *req.Msg.Cursor != "" {
		var cursorComment model.Comment
		if err := s.db.WithContext(ctx).
			Select("created_at").
			Where("id = ? AND post_id = ? AND parent_id = ?", *req.Msg.Cursor, parentComment.PostID, req.Msg.CommentId).
			First(&cursorComment).Error; err == nil {
			query = query.Where("c.created_at > ? OR (c.created_at = ? AND c.id > ?)",
				cursorComment.CreatedAt, cursorComment.CreatedAt, *req.Msg.Cursor)
		}
	}

	// Fetch limit + 1 to know if there are more
	var replies []model.CommentWithAuthor
	if err := query.Limit(limit + 1).Find(&replies).Error; err != nil {
		return nil, errs.Internal(err)
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
			return nil, errs.Internal(err)
		}
	}

	summaries, err := s.commentMemberSummaries(ctx, replies, nestedReplies)
	if err != nil {
		return nil, errs.Internal(err)
	}
	tree := s.buildCommentTreeWithPagination(replies, nestedReplies, defaultReplyLimit, summaries)

	return connect.NewResponse(&managev1.LoadMoreRepliesResponse{
		Replies:    tree,
		NextCursor: nextCursor,
		HasMore:    hasMore,
		TotalCount: int32(totalCount),
	}), nil
}

// CreateComment creates a new comment
func (s *CommentService) CreateComment(
	ctx context.Context,
	req *connect.Request[managev1.CreateCommentRequest],
) (*connect.Response[managev1.CreateCommentResponse], error) {
	user := auth.GetUser(ctx)
	if user == nil || user.MemberID.String() == "" {
		return nil, errs.AuthenticationRequired()
	}
	if err := validateCreateCommentRequest(req.Msg); err != nil {
		return nil, err
	}
	var comment model.Comment
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		created, err := s.createCommentWithDB(ctx, tx, user, req.Msg)
		comment = created
		return err
	}); err != nil {
		return nil, err
	}
	result, err := s.loadCommentWithAuthor(ctx, comment.ID)
	if err != nil {
		return nil, errs.Internal(err)
	}
	summaries, err := s.commentMemberSummaries(ctx, []model.CommentWithAuthor{result}, nil)
	if err != nil {
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&managev1.CreateCommentResponse{
		Comment: s.toProtoCommentWithAuthor(&result, summaries),
	}), nil
}

func validateCreateCommentRequest(request *managev1.CreateCommentRequest) error {
	if request.PostId == "" {
		return errs.Required("post_id")
	}
	if request.Content == "" {
		return errs.Required("content")
	}
	if len(request.Content) > maxContentLen {
		return errs.InvalidArgument("content", fmt.Sprintf("exceeds maximum length of %d characters", maxContentLen))
	}
	return nil
}

func (s *CommentService) createCommentWithDB(
	ctx context.Context,
	tx *gorm.DB,
	user *auth.UserInfo,
	request *managev1.CreateCommentRequest,
) (model.Comment, error) {
	if err := lockCommentablePost(ctx, tx, request.PostId); err != nil {
		return model.Comment{}, err
	}
	if err := requireActiveCommentPrincipal(ctx, tx, user); err != nil {
		return model.Comment{}, err
	}
	parentID, err := s.resolveCommentParentWithDB(ctx, tx, request.PostId, request.ParentId)
	if err != nil {
		return model.Comment{}, err
	}
	memberID := user.MemberID.String()
	comment := model.Comment{
		PostID: request.PostId, MemberID: &memberID, ParentID: parentID, Content: request.Content,
	}
	if err := tx.Create(&comment).Error; err != nil {
		return model.Comment{}, errs.Internal(err)
	}
	return comment, nil
}

func lockCommentablePost(ctx context.Context, tx *gorm.DB, postID string) error {
	var post model.Post
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id, status, comments_enabled").Where("id = ?", postID).First(&post).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return errs.NotFoundMsg("post not found")
		}
		return errs.Internal(err)
	}
	if !post.CommentsEnabled {
		return errs.FailedPrecondition(errs.MsgCommentsDisabled)
	}
	if post.Status != model.PostStatus(managev1.PostStatus_POST_STATUS_PUBLISHED.String()) {
		return errs.FailedPrecondition(errs.MsgCannotCommentUnpublished)
	}
	return nil
}

func requireActiveCommentPrincipal(ctx context.Context, tx *gorm.DB, user *auth.UserInfo) error {
	active, err := identitystate.LockActivePrincipal(ctx, tx, user)
	if err != nil {
		return errs.Internal(fmt.Errorf("lock comment author: %w", err))
	}
	if !active {
		return errs.PermissionDenied("comment authority was revoked")
	}
	return nil
}

func (s *CommentService) resolveCommentParentWithDB(
	ctx context.Context,
	tx *gorm.DB,
	postID string,
	requestedParentID *string,
) (*string, error) {
	if requestedParentID == nil || *requestedParentID == "" {
		return nil, nil
	}
	var parent model.Comment
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND post_id = ?", *requestedParentID, postID).First(&parent).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFoundMsg("parent comment not found")
		}
		return nil, errs.Internal(err)
	}
	depth, err := s.getCommentDepthTx(tx, ctx, parent.ID, postID)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if depth >= maxCommentDepth {
		return nil, errs.MaxCommentDepth(maxCommentDepth)
	}
	return &parent.ID, nil
}

func (s *CommentService) loadCommentWithAuthor(ctx context.Context, commentID string) (model.CommentWithAuthor, error) {
	var comment model.CommentWithAuthor
	err := s.db.WithContext(ctx).Table("comment c").
		Select(`c.id, c.post_id, c.member_id, c.parent_id, c.content, c.is_deleted,
			c.created_at, c.updated_at`).
		Where("c.id = ?", commentID).First(&comment).Error
	return comment, err
}

// UpdateComment updates a comment's content
func (s *CommentService) UpdateComment(
	ctx context.Context,
	req *connect.Request[managev1.UpdateCommentRequest],
) (*connect.Response[managev1.UpdateCommentResponse], error) {
	// Get authenticated user
	user := auth.GetUser(ctx)
	if user == nil || user.MemberID.String() == "" {
		return nil, errs.AuthenticationRequired()
	}
	memberID := user.MemberID.String()

	// Validate input
	if req.Msg.Id == "" {
		return nil, errs.Required("id")
	}
	if req.Msg.Content == "" {
		return nil, errs.Required("content")
	}
	if len(req.Msg.Content) > maxContentLen {
		return nil, errs.InvalidArgument("content", fmt.Sprintf("exceeds maximum length of %d characters", maxContentLen))
	}

	var result model.CommentWithAuthor
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		active, err := identitystate.LockActivePrincipal(ctx, tx, user)
		if err != nil {
			return errs.Internal(fmt.Errorf("lock comment author: %w", err))
		}
		if !active {
			return errs.PermissionDenied("comment authority was revoked")
		}
		var comment model.Comment
		if err := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", req.Msg.Id).
			First(&comment).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFoundMsg("comment not found")
			}
			return errs.Internal(err)
		}
		if comment.IsDeleted {
			return errs.FailedPrecondition(errs.MsgCannotUpdateDeleted)
		}
		isAuthor := comment.MemberID != nil && *comment.MemberID == memberID
		if !isAuthor {
			return errs.PermissionDenied("permission denied")
		}
		if err := tx.Model(&comment).Update("content", req.Msg.Content).Error; err != nil {
			return errs.Internal(err)
		}
		if err := tx.WithContext(ctx).
			Table("comment c").
			Select(`c.id, c.post_id, c.member_id, c.parent_id, c.content, c.is_deleted,
				c.created_at, c.updated_at`).
			Where("c.id = ?", comment.ID).
			First(&result).Error; err != nil {
			return errs.Internal(err)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	summaries, err := s.commentMemberSummaries(ctx, []model.CommentWithAuthor{result}, nil)
	if err != nil {
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&managev1.UpdateCommentResponse{
		Comment: s.toProtoCommentWithAuthor(&result, summaries),
	}), nil
}

// DeleteComment soft-deletes a comment
func (s *CommentService) DeleteComment(
	ctx context.Context,
	req *connect.Request[managev1.DeleteCommentRequest],
) (*connect.Response[managev1.DeleteCommentResponse], error) {
	// Get authenticated user
	user := auth.GetUser(ctx)
	if user == nil || user.MemberID.String() == "" {
		return nil, errs.AuthenticationRequired()
	}
	memberID := user.MemberID.String()

	// Validate input
	if req.Msg.Id == "" {
		return nil, errs.Required("id")
	}
	var target struct {
		PostID string `gorm:"column:post_id"`
	}
	if err := s.db.WithContext(ctx).Table("comment").Select("post_id").Where("id = ?", req.Msg.Id).Take(&target).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFoundMsg("comment not found")
		}
		return nil, errs.Internal(err)
	}

	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		postStatus, err := lockPostParticipantRoot(ctx, tx, target.PostID)
		if err != nil {
			return err
		}
		active, err := identitystate.LockActivePrincipal(ctx, tx, user)
		if err != nil {
			return errs.Internal(fmt.Errorf("lock comment deletion principal: %w", err))
		}
		if !active {
			return errs.PermissionDenied("comment authority was revoked")
		}
		var comment model.Comment
		if err := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", req.Msg.Id).
			First(&comment).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFoundMsg("comment not found")
			}
			return errs.Internal(err)
		}
		isAuthor := comment.MemberID != nil && *comment.MemberID == memberID
		if !isAuthor {
			if _, err := requireLockedPostActionForStatus(ctx, tx, s.spiceDB, comment.PostID, postStatus, policyv1.Post.Manage); err != nil {
				return err
			}
		}
		if err := tx.Model(&comment).Updates(structured.Fields{
			"is_deleted": true,
			"content":    "[This comment has been deleted]",
		}).Error; err != nil {
			return errs.Internal(err)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	return connect.NewResponse(&managev1.DeleteCommentResponse{
		Success: true,
	}), nil
}

func requirePublicCommentPost(post model.Post) error {
	if post.Status == model.PostStatus(managev1.PostStatus_POST_STATUS_PUBLISHED.String()) ||
		post.Status == model.PostStatus(managev1.PostStatus_POST_STATUS_ARCHIVED.String()) {
		return nil
	}
	return errs.NotFoundMsg("post not found")
}

// Helper to get comment depth using recursive CTE within a transaction
func (s *CommentService) getCommentDepthTx(tx *gorm.DB, ctx context.Context, commentID string, postID string) (int, error) {
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
func (s *CommentService) buildCommentTreeWithPagination(
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
		nodeMap[topLevel[i].ID] = s.toProtoCommentNode(&topLevel[i], summaries)
	}

	// Second pass: create nodes for all replies and count direct children
	for i := range allReplies {
		nodeMap[allReplies[i].ID] = s.toProtoCommentNode(&allReplies[i], summaries)
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
func (s *CommentService) toProtoCommentNode(c *model.CommentWithAuthor, summaries map[string]*commonv1.MemberSummary) *managev1.CommentNode {
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
func (s *CommentService) toProtoCommentWithAuthor(c *model.CommentWithAuthor, summaries map[string]*commonv1.MemberSummary) *managev1.CommentWithAuthor {
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

func (s *CommentService) commentMemberSummaries(
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
