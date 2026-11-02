package posttest

import (
	"context"
	"slices"
	"sort"
	"time"

	"github.com/google/uuid"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/post"
)

func parentMatches(comment post.Comment, parentID *string) bool {
	if parentID == nil {
		return comment.ParentID == nil
	}
	return comment.ParentID != nil && *comment.ParentID == *parentID
}

func sortComments(comments []post.Comment) {
	sort.Slice(comments, func(i, j int) bool {
		if !comments[i].CreatedAt.Equal(comments[j].CreatedAt) {
			return comments[i].CreatedAt.Before(comments[j].CreatedAt)
		}
		return comments[i].ID < comments[j].ID
	})
}

// afterCursor reports whether comment sorts after the cursor.
func afterCursor(comment post.Comment, cursor post.CommentCursorAt) bool {
	if comment.CreatedAt.Equal(cursor.CreatedAt) {
		return comment.ID > cursor.ID
	}
	return comment.CreatedAt.After(cursor.CreatedAt)
}

func (s *Store) CountComments(_ context.Context, postID string, parentID *string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int64
	for _, comment := range s.comments {
		if comment.PostID == postID && parentMatches(comment, parentID) {
			count++
		}
	}
	return count, nil
}

func (s *Store) CommentCursor(_ context.Context, postID string, parentID *string, cursorID string) (time.Time, bool, error) {
	if err := s.fail("CommentCursor"); err != nil {
		return time.Time{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	comment, ok := s.comments[cursorID]
	if !ok || comment.PostID != postID || !parentMatches(comment, parentID) {
		return time.Time{}, false, nil
	}
	return comment.CreatedAt, true, nil
}

func (s *Store) ListComments(_ context.Context, postID string, parentID *string, after *post.CommentCursorAt, limit int) ([]post.Comment, error) {
	s.mu.Lock()
	var comments []post.Comment
	for _, comment := range s.comments {
		if comment.PostID != postID || !parentMatches(comment, parentID) {
			continue
		}
		if after != nil && !afterCursor(comment, *after) {
			continue
		}
		comments = append(comments, comment)
	}
	s.mu.Unlock()
	sortComments(comments)
	return page(comments, 0, limit), nil
}

func (s *Store) CommentDescendants(_ context.Context, postID string, parentIDs []string, maxDepth int) ([]post.Comment, error) {
	s.mu.Lock()
	var descendants []post.Comment
	frontier := slices.Clone(parentIDs)
	for depth := 1; depth <= maxDepth && len(frontier) != 0; depth++ {
		var next []string
		for _, comment := range s.comments {
			if comment.PostID == postID && comment.ParentID != nil && slices.Contains(frontier, *comment.ParentID) {
				descendants = append(descendants, comment)
				next = append(next, comment.ID)
			}
		}
		frontier = next
	}
	s.mu.Unlock()
	sortComments(descendants)
	return descendants, nil
}

func (s *Store) FindComment(_ context.Context, commentID string) (post.Comment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	comment, ok := s.comments[commentID]
	if !ok {
		return post.Comment{}, post.ErrCommentNotFound
	}
	return comment, nil
}

func (s *Store) FindPostComment(ctx context.Context, postID, commentID string) (post.Comment, error) {
	comment, err := s.FindComment(ctx, commentID)
	if err == nil && comment.PostID != postID {
		return post.Comment{}, post.ErrCommentNotFound
	}
	return comment, err
}

func (s *Store) commentPost(postID string) (post.CommentPost, error) {
	stored, err := s.lookup(postID)
	if err != nil {
		return post.CommentPost{}, err
	}
	return post.CommentPost{ID: stored.ID, Status: stored.Status, CommentsEnabled: stored.CommentsEnabled}, nil
}

func (s *Store) ShareLockCommentPost(_ context.Context, postID string) (post.CommentPost, error) {
	return s.commentPost(postID)
}

func (s *Store) LockCommentPost(_ context.Context, postID string) (post.CommentPost, error) {
	return s.commentPost(postID)
}

func (s *Store) LockComment(ctx context.Context, commentID string) (post.Comment, error) {
	return s.FindComment(ctx, commentID)
}

func (s *Store) LockPostComment(ctx context.Context, postID, commentID string) (post.Comment, error) {
	return s.FindPostComment(ctx, postID, commentID)
}

func (s *Store) CommentDepth(_ context.Context, postID, commentID string, maxDepth int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	depth := 0
	current, ok := s.comments[commentID]
	for ok && current.PostID == postID && depth < maxDepth {
		depth++
		if current.ParentID == nil {
			break
		}
		current, ok = s.comments[*current.ParentID]
	}
	return depth, nil
}

func (s *Store) CommentPostID(ctx context.Context, commentID string) (string, error) {
	comment, err := s.FindComment(ctx, commentID)
	return comment.PostID, err
}

func (s *Store) InsertComment(_ context.Context, comment *post.Comment) error {
	if err := s.fail("InsertComment"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if comment.ID == "" {
		comment.ID = uuid.NewString()
	}
	if comment.CreatedAt.IsZero() {
		s.sequence++
		comment.CreatedAt = s.now().Add(time.Duration(s.sequence) * time.Millisecond)
	}
	if comment.UpdatedAt.IsZero() {
		comment.UpdatedAt = comment.CreatedAt
	}
	s.comments[comment.ID] = *comment
	return nil
}

func (s *Store) UpdateCommentContent(_ context.Context, comment post.Comment, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.comments[comment.ID]
	if ok {
		stored.Content, stored.UpdatedAt = content, s.now()
		s.comments[comment.ID] = stored
	}
	return nil
}

func (s *Store) SoftDeleteComment(_ context.Context, comment post.Comment, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.comments[comment.ID]
	if ok {
		stored.IsDeleted, stored.Content, stored.UpdatedAt = true, content, s.now()
		s.comments[comment.ID] = stored
	}
	return nil
}

func (s *Store) ListVersions(_ context.Context, postID string, limit, offset int32) ([]post.StoredVersion, int64, error) {
	s.mu.Lock()
	versions := slices.Clone(s.versions[postID])
	s.mu.Unlock()
	sort.Slice(versions, func(i, j int) bool { return versions[i].Version > versions[j].Version })
	return page(versions, int(offset), int(limit)), int64(len(versions)), nil
}

func (s *Store) FindVersion(_ context.Context, versionID string) (post.StoredVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, versions := range s.versions {
		for _, version := range versions {
			if version.ID == versionID {
				return version, nil
			}
		}
	}
	return post.StoredVersion{}, post.ErrVersionNotFound
}

func (s *Store) LockLatestVersion(_ context.Context, postID string) (post.StoredVersion, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var latest post.StoredVersion
	found := false
	for _, version := range s.versions[postID] {
		if !found || version.Version > latest.Version {
			latest, found = version, true
		}
	}
	return latest, found, nil
}

func (s *Store) InsertVersion(_ context.Context, version *post.StoredVersion) error {
	if err := s.fail("InsertVersion"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.versions[version.PostID] {
		if existing.Version == version.Version {
			return errs.AlreadyExistsMsg("post version already exists")
		}
	}
	version.ID = uuid.NewString()
	version.CreatedAt = s.now()
	version.ContributorMemberIDs = slices.Clone(version.ContributorMemberIDs)
	s.versions[version.PostID] = append(s.versions[version.PostID], *version)
	return nil
}
