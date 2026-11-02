package post

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/memberref"
	"github.com/echovisionlab/geul-api/internal/mutation"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
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

// deletedCommentContent replaces the content of a deleted comment.
const deletedCommentContent = "[This comment has been deleted]"

// CommentDeps are the collaborators of the comment use cases. Every field
// is required; NewCommentService rejects a missing one.
type CommentDeps struct {
	Units   mutation.UnitOfWork[Store] // postpostgres.NewUnits
	SpiceDB mutation.Authorizer
	Fence   identitystate.Fence // identitystate.PostgresFence{}
	Members MemberSummaryLoader
	Audit   domainaudit.Appender
}

// CommentService holds the use cases of Post comments.
type CommentService struct {
	units     mutation.UnitOfWork[Store]
	spiceDB   mutation.Authorizer
	members   MemberSummaryLoader
	mutations *mutation.StoreRunner[Store]
}

// NewCommentService validates deps and returns the comment use cases.
func NewCommentService(deps CommentDeps) (*CommentService, error) {
	var missing []string
	for name, absent := range map[string]bool{
		"Units": deps.Units == nil, "SpiceDB": deps.SpiceDB == nil, "Fence": deps.Fence == nil,
		"Members": deps.Members == nil, "Audit": deps.Audit == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return nil, errors.New("comment dependencies are required: " + strings.Join(missing, ", "))
	}
	mutations, err := mutation.NewStoreRunner(deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &CommentService{units: deps.Units, spiceDB: deps.SpiceDB, members: deps.Members, mutations: mutations}, nil
}

// commentReader reads comments in a snapshot that already authorized their
// Post.
type commentReader struct {
	store   CommentReads
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

// CommentNode is a comment with its author and the first page of its
// replies.
type CommentNode struct {
	Comment
	Author          *memberref.Summary
	Replies         []*CommentNode
	HasMoreReplies  bool
	TotalReplyCount int32
}

// CommentPage is one page of comments.
type CommentPage struct {
	Comments   []*CommentNode
	NextCursor *string
	HasMore    bool
	TotalCount int32
}

// AuthoredComment is a comment with its author.
type AuthoredComment struct {
	Comment
	Author *memberref.Summary
}

// ListCommentsByPost returns comments for a post with Reddit-style pagination
func (s *CommentService) ListCommentsByPost(ctx context.Context, query ListCommentsQuery) (_ CommentPage, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", query.PostID))

	// Require authentication for Secure API
	user := auth.GetUser(ctx)
	if user == nil {
		return CommentPage{}, domainerr.AuthenticationRequired()
	}

	if query.PostID == "" {
		return CommentPage{}, domainerr.Required("post_id")
	}

	var page CommentPage
	err := s.withReadablePost(ctx, query.PostID, func(store Store) error {
		var err error
		page, err = ReadComments(ctx, store, s.members, query)
		return err
	})
	return page, err
}

// ReadComments reads within the caller's authorized Post snapshot, which
// store is bound to. The caller must hold the Post root lock and check
// visibility and comments_enabled.
func ReadComments(ctx context.Context, store CommentReads, members MemberSummaryLoader, query ListCommentsQuery) (CommentPage, error) {
	return commentReader{store: store, members: members}.readComments(ctx, query)
}

func (s commentReader) readComments(ctx context.Context, request ListCommentsQuery) (CommentPage, error) {
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
	totalCount, err := s.store.CountComments(ctx, request.PostID, nil)
	if err != nil {
		return CommentPage{}, err
	}

	after, err := s.commentCursor(ctx, request.PostID, request.Cursor)
	if err != nil {
		return CommentPage{}, err
	}

	// Fetch limit + 1 to know if there are more
	topLevelComments, err := s.store.ListComments(ctx, request.PostID, nil, after, limit+1)
	if err != nil {
		return CommentPage{}, err
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
	var allReplies []Comment
	if len(topLevelIDs) > 0 {
		if allReplies, err = s.store.CommentDescendants(ctx, request.PostID, topLevelIDs, maxCommentDepth); err != nil {
			return CommentPage{}, err
		}
	}

	summaries, err := s.commentMemberSummaries(ctx, topLevelComments, allReplies)
	if err != nil {
		return CommentPage{}, err
	}
	tree := buildCommentTreeWithPagination(topLevelComments, allReplies, replyLimit, summaries)

	return CommentPage{
		Comments:   tree,
		NextCursor: nextCursor,
		HasMore:    hasMore,
		TotalCount: int32(totalCount),
	}, nil
}

// commentCursor is where a top-level page continues; a cursor that names
// no top-level comment of the Post starts from the beginning.
func (s commentReader) commentCursor(ctx context.Context, postID string, cursor *string) (*CommentCursorAt, error) {
	if cursor == nil || *cursor == "" {
		return nil, nil
	}
	createdAt, found, err := s.store.CommentCursor(ctx, postID, nil, *cursor)
	if err != nil || !found {
		return nil, err
	}
	return &CommentCursorAt{ID: *cursor, CreatedAt: createdAt}, nil
}

// LoadMoreReplies returns more replies for a specific comment
func (s *CommentService) LoadMoreReplies(ctx context.Context, request LoadMoreRepliesQuery) (_ CommentPage, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("comment_id", request.CommentID))

	// Require authentication for Secure API
	user := auth.GetUser(ctx)
	if user == nil {
		return CommentPage{}, domainerr.AuthenticationRequired()
	}

	if request.CommentID == "" {
		return CommentPage{}, domainerr.Required("comment_id")
	}

	// Find the parent comment and verify it exists
	var parentComment Comment
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		parentComment, err = store.FindComment(ctx, request.CommentID)
		return err
	}); err != nil {
		if errors.Is(err, ErrCommentNotFound) {
			return CommentPage{}, domainerr.New(domainerr.NotFound, "", "comment not found")
		}
		return CommentPage{}, err
	}

	var page CommentPage
	err := s.withReadablePost(ctx, parentComment.PostID, func(store Store) error {
		var err error
		page, err = ReadCommentReplies(ctx, store, s.members, request, parentComment)
		return err
	})
	return page, err
}

// ReadCommentReplies reads replies within the caller's authorized Post
// snapshot, which store is bound to.
func ReadCommentReplies(ctx context.Context, store CommentReads, members MemberSummaryLoader, query LoadMoreRepliesQuery, parentComment Comment) (CommentPage, error) {
	return commentReader{store: store, members: members}.readCommentReplies(ctx, query, parentComment)
}

func (s commentReader) readCommentReplies(ctx context.Context, request LoadMoreRepliesQuery, parentComment Comment) (CommentPage, error) {
	// Normalize limit
	limit := int(request.Limit)
	if limit <= 0 {
		limit = 10
	}
	if limit > maxReplyLimit {
		limit = maxReplyLimit
	}

	// Count total direct replies
	totalCount, err := s.store.CountComments(ctx, parentComment.PostID, &request.CommentID)
	if err != nil {
		return CommentPage{}, err
	}

	// Apply cursor if provided
	var after *CommentCursorAt
	if request.Cursor != nil && *request.Cursor != "" {
		createdAt, found, err := s.store.CommentCursor(ctx, parentComment.PostID, &request.CommentID, *request.Cursor)
		if err != nil {
			slog.ErrorContext(ctx, "Reply cursor lookup failed; listing replies from the start", "domain", "post", "operation", "load_reply_cursor", "outcome", "failed", failurecause.Attr(err), "comment_id", request.CommentID, "cursor_id", *request.Cursor)
		} else if found {
			after = &CommentCursorAt{ID: *request.Cursor, CreatedAt: createdAt}
		}
	}

	// Fetch limit + 1 to know if there are more
	replies, err := s.store.ListComments(ctx, parentComment.PostID, &request.CommentID, after, limit+1)
	if err != nil {
		return CommentPage{}, err
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

	var nestedReplies []Comment
	if len(replyIDs) > 0 {
		// Get all descendants
		if nestedReplies, err = s.store.CommentDescendants(ctx, parentComment.PostID, replyIDs, maxCommentDepth); err != nil {
			return CommentPage{}, err
		}
	}

	summaries, err := s.commentMemberSummaries(ctx, replies, nestedReplies)
	if err != nil {
		return CommentPage{}, err
	}
	tree := buildCommentTreeWithPagination(replies, nestedReplies, defaultReplyLimit, summaries)

	return CommentPage{
		Comments:   tree,
		NextCursor: nextCursor,
		HasMore:    hasMore,
		TotalCount: int32(totalCount),
	}, nil
}

// commentMutation runs change, which a comment's author or a Post manager
// may make, and audits operation on the changed comment as a change of its
// Post's comments by the request's member; the comment is read back for the
// response.
func (s *CommentService) commentMutation(ctx context.Context, access mutation.Access, operation sharedtelemetry.AuditItemOperation, change func(context.Context, Store) (Comment, error)) (Comment, error) {
	return mutation.Run(ctx, s.mutations, mutation.StoreSpec[Comment, Store]{
		Access: access,
		Change: change,
		Audit: func(comment Comment) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditPostUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewPostCommentAuditRecord(metadata, comment.PostID, comment.ID, operation)
			})
		},
	})
}

// CreateComment creates a new comment
func (s *CommentService) CreateComment(ctx context.Context, command CreateCommentCommand) (AuthoredComment, error) {
	user := auth.GetUser(ctx)
	if user == nil || user.MemberID.String() == "" {
		return AuthoredComment{}, domainerr.AuthenticationRequired()
	}
	if err := validateCreateCommentCommand(command); err != nil {
		return AuthoredComment{}, err
	}
	access := mutation.Fenced(func(ctx context.Context, store Store, lockActive func(*auth.UserInfo) (bool, error)) error {
		if err := lockCommentablePost(ctx, store, command.PostID); err != nil {
			return err
		}
		return requireActiveCommentPrincipal(lockActive, user)
	})
	created, err := s.commentMutation(ctx, access, sharedtelemetry.AuditItemOperationCreated, func(ctx context.Context, store Store) (Comment, error) {
		return createComment(ctx, store, user, command)
	})
	if err != nil {
		return AuthoredComment{}, err
	}
	result, err := readComment(ctx, s.units, created.ID)
	if err != nil {
		return AuthoredComment{}, err
	}
	return s.authored(ctx, result)
}

// readComment reads a comment back in a snapshot of its own.
func readComment(ctx context.Context, units mutation.UnitOfWork[Store], commentID string) (Comment, error) {
	var comment Comment
	err := units.Read(ctx, func(store Store) error {
		var err error
		comment, err = store.FindComment(ctx, commentID)
		return err
	})
	return comment, err
}

func (s *CommentService) authored(ctx context.Context, comment Comment) (AuthoredComment, error) {
	summaries, err := commentReader{members: s.members}.commentMemberSummaries(ctx, []Comment{comment}, nil)
	if err != nil {
		return AuthoredComment{}, err
	}
	authored := AuthoredComment{Comment: comment}
	if comment.MemberID != nil {
		authored.Author = summaries[*comment.MemberID]
	}
	return authored, nil
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

func createComment(
	ctx context.Context,
	store Store,
	user *auth.UserInfo,
	command CreateCommentCommand,
) (Comment, error) {
	parentID, err := resolveCommentParent(ctx, store, command.PostID, command.ParentID)
	if err != nil {
		return Comment{}, err
	}
	memberID := user.MemberID.String()
	comment := Comment{
		PostID: command.PostID, MemberID: &memberID, ParentID: parentID, Content: command.Content,
	}
	if err := store.InsertComment(ctx, &comment); err != nil {
		return Comment{}, err
	}
	return comment, nil
}

func lockCommentablePost(ctx context.Context, store Store, postID string) error {
	commented, err := store.LockCommentPost(ctx, postID)
	if err != nil {
		if errors.Is(err, ErrPostNotFound) {
			return domainerr.New(domainerr.NotFound, "", "post not found")
		}
		return err
	}
	if !commented.CommentsEnabled {
		return domainerr.Precondition(msgCommentsDisabled)
	}
	if commented.Status != StatusPublished {
		return domainerr.Precondition(msgCannotCommentUnpublished)
	}
	return nil
}

func requireActiveCommentPrincipal(lockActive func(*auth.UserInfo) (bool, error), user *auth.UserInfo) error {
	active, err := lockActive(user)
	if err != nil {
		return fmt.Errorf("lock comment author: %w", err)
	}
	if !active {
		return domainerr.Denied("comment authority was revoked")
	}
	return nil
}

func resolveCommentParent(
	ctx context.Context,
	store Store,
	postID string,
	requestedParentID *string,
) (*string, error) {
	if requestedParentID == nil || *requestedParentID == "" {
		return nil, nil
	}
	parent, err := store.LockPostComment(ctx, postID, *requestedParentID)
	if err != nil {
		if errors.Is(err, ErrCommentNotFound) {
			return nil, domainerr.New(domainerr.NotFound, "", "parent comment not found")
		}
		return nil, err
	}
	depth, err := store.CommentDepth(ctx, postID, parent.ID, maxCommentDepth)
	if err != nil {
		return nil, err
	}
	if depth >= maxCommentDepth {
		return nil, domainerr.Precondition(fmt.Sprintf("maximum comment depth of %d reached", maxCommentDepth))
	}
	return &parent.ID, nil
}

// UpdateComment updates a comment's content
func (s *CommentService) UpdateComment(ctx context.Context, command UpdateCommentCommand) (_ AuthoredComment, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("comment_id", command.ID))

	user := auth.GetUser(ctx)
	if user == nil || user.MemberID.String() == "" {
		return AuthoredComment{}, domainerr.AuthenticationRequired()
	}
	memberID := user.MemberID.String()
	if command.ID == "" {
		return AuthoredComment{}, domainerr.Required("id")
	}
	if command.Content == "" {
		return AuthoredComment{}, domainerr.Required("content")
	}
	if len(command.Content) > maxContentLen {
		return AuthoredComment{}, domainerr.InvalidField("content", fmt.Sprintf("exceeds maximum length of %d characters", maxContentLen))
	}
	var result Comment
	_, err := s.commentMutation(ctx, mutation.Fenced(func(_ context.Context, _ Store, lockActive func(*auth.UserInfo) (bool, error)) error {
		return requireActiveCommentPrincipal(lockActive, user)
	}), sharedtelemetry.AuditItemOperationUpdated, func(ctx context.Context, store Store) (Comment, error) {
		comment, err := lockComment(ctx, store, command.ID)
		if err != nil {
			return comment, err
		}
		if comment.IsDeleted {
			return comment, domainerr.Precondition(msgCannotUpdateDeleted)
		}
		if comment.MemberID == nil || *comment.MemberID != memberID {
			return comment, domainerr.Denied("permission denied")
		}
		if err := store.UpdateCommentContent(ctx, comment, command.Content); err != nil {
			return comment, err
		}
		result, err = store.FindComment(ctx, comment.ID)
		return comment, err
	})
	if err != nil {
		return AuthoredComment{}, err
	}
	return s.authored(ctx, result)
}

func lockComment(ctx context.Context, store Store, commentID string) (Comment, error) {
	comment, err := store.LockComment(ctx, commentID)
	if errors.Is(err, ErrCommentNotFound) {
		return comment, domainerr.New(domainerr.NotFound, "", "comment not found")
	}
	return comment, err
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
	var postID string
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		postID, err = store.CommentPostID(ctx, command.ID)
		return err
	}); err != nil {
		if errors.Is(err, ErrCommentNotFound) {
			return domainerr.New(domainerr.NotFound, "", "comment not found")
		}
		return err
	}
	var comment Comment
	// The author deletes their own comment; anyone else needs to manage the
	// Post, checked as RequireFreshCan checks it once the principal is
	// locked.
	access := mutation.Fenced(func(ctx context.Context, store Store, lockActive func(*auth.UserInfo) (bool, error)) error {
		postStatus, err := lockPostParticipantRoot(ctx, store, postID)
		if err != nil {
			return err
		}
		active, err := lockActive(user)
		if err != nil {
			return fmt.Errorf("lock comment deletion principal: %w", err)
		}
		if !active {
			return domainerr.Denied("comment authority was revoked")
		}
		if comment, err = lockComment(ctx, store, command.ID); err != nil {
			return err
		}
		if comment.MemberID != nil && *comment.MemberID == memberID {
			return nil
		}
		_, err = requireLockedPostActionForStatus(func(can policyv1.Can) error {
			return authz.RequireCan(ctx, s.spiceDB, can)
		}, comment.PostID, postStatus, policyv1.Post.Manage)
		return err
	})
	_, err := s.commentMutation(ctx, access, sharedtelemetry.AuditItemOperationDeleted, func(ctx context.Context, store Store) (Comment, error) {
		return comment, store.SoftDeleteComment(ctx, comment, deletedCommentContent)
	})
	return err
}

// withReadablePost runs read in one transaction after locking the Post and
// checking the caller may read its comments.
func (s *CommentService) withReadablePost(ctx context.Context, postID string, read func(Store) error) error {
	access := mutation.Checked(func(ctx context.Context, store Store) error {
		commented, err := store.ShareLockCommentPost(ctx, postID)
		if err != nil {
			if errors.Is(err, ErrPostNotFound) {
				return domainerr.New(domainerr.NotFound, "", "post not found")
			}
			return err
		}
		return requireCommentPostView(ctx, s.spiceDB, commented)
	})
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[struct{}, Store]{
		Access: access,
		Change: func(_ context.Context, store Store) (struct{}, error) { return struct{}{}, read(store) },
		Audit:  func(struct{}) mutation.Audit { return mutation.Unchanged() },
	})
	return err
}

func requireCommentPostView(ctx context.Context, checker authz.AuthorizationDecisionChecker, commented CommentPost) error {
	if commented.Status != StatusPublished &&
		commented.Status != StatusArchived {
		if _, err := requirePostViewForStatus(ctx, checker, commented.ID, commented.Status); err != nil {
			return err
		}
	}
	if !commented.CommentsEnabled {
		return domainerr.Precondition(msgCommentsDisabled)
	}
	return nil
}

// Helper to build tree structure with reply pagination (Reddit-style)
func buildCommentTreeWithPagination(
	topLevel []Comment,
	allReplies []Comment,
	replyLimit int,
	summaries map[string]*memberref.Summary,
) []*CommentNode {
	// Create map for all comments
	nodeMap := make(map[string]*CommentNode)
	replyCountMap := make(map[string]int) // Direct reply count per parent

	// First pass: create nodes for top-level comments
	for i := range topLevel {
		nodeMap[topLevel[i].ID] = commentNode(topLevel[i], summaries)
	}

	// Second pass: create nodes for all replies and count direct children
	for i := range allReplies {
		nodeMap[allReplies[i].ID] = commentNode(allReplies[i], summaries)
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
	result := make([]*CommentNode, len(topLevel))
	for i := range topLevel {
		result[i] = nodeMap[topLevel[i].ID]
	}

	return result
}

func commentNode(comment Comment, summaries map[string]*memberref.Summary) *CommentNode {
	node := &CommentNode{Comment: comment, Replies: []*CommentNode{}}
	if comment.MemberID != nil {
		node.Author = summaries[*comment.MemberID]
	}
	return node
}

func (s commentReader) commentMemberSummaries(
	ctx context.Context,
	groups ...[]Comment,
) (map[string]*memberref.Summary, error) {
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
