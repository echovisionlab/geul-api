package postpostgres

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/structured"
)

func commentFromRow(row model.Comment) post.Comment {
	return post.Comment{
		ID: row.ID, PostID: row.PostID, MemberID: row.MemberID, ParentID: row.ParentID, Content: row.Content,
		IsDeleted: row.IsDeleted, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func commentRow(comment post.Comment) model.Comment {
	return model.Comment{
		ID: comment.ID, PostID: comment.PostID, MemberID: comment.MemberID, ParentID: comment.ParentID,
		Content: comment.Content, IsDeleted: comment.IsDeleted, CreatedAt: comment.CreatedAt, UpdatedAt: comment.UpdatedAt,
	}
}

func commentsFromRows(rows []model.CommentWithAuthor) []post.Comment {
	comments := make([]post.Comment, len(rows))
	for index, row := range rows {
		comments[index] = post.Comment{
			ID: row.ID, PostID: row.PostID, MemberID: row.MemberID, ParentID: row.ParentID, Content: row.Content,
			IsDeleted: row.IsDeleted, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		}
	}
	return comments
}

func (s *store) CountComments(ctx context.Context, postID string, parentID *string) (int64, error) {
	query := s.tx.WithContext(ctx).Model(&model.Comment{})
	if parentID == nil {
		query = query.Where("post_id = ? AND parent_id IS NULL", postID)
	} else {
		query = query.Where("post_id = ? AND parent_id = ?", postID, *parentID)
	}
	var count int64
	err := query.Count(&count).Error
	return count, err
}

func (s *store) CommentCursor(ctx context.Context, postID string, parentID *string, cursorID string) (time.Time, bool, error) {
	query := s.tx.WithContext(ctx).Select("created_at")
	if parentID == nil {
		query = query.Where("id = ? AND post_id = ? AND parent_id IS NULL", cursorID, postID)
	} else {
		query = query.Where("id = ? AND post_id = ? AND parent_id = ?", cursorID, postID, *parentID)
	}
	var cursor model.Comment
	if err := query.First(&cursor).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, err
	}
	return cursor.CreatedAt, true, nil
}

const commentColumns = `c.id, c.post_id, c.member_id, c.parent_id, c.content, c.is_deleted,
				c.created_at, c.updated_at`

func (s *store) ListComments(ctx context.Context, postID string, parentID *string, after *post.CommentCursorAt, limit int) ([]post.Comment, error) {
	query := s.tx.WithContext(ctx).
		Table("comment c").
		Select(commentColumns)
	if parentID == nil {
		query = query.Where("c.post_id = ? AND c.parent_id IS NULL", postID)
	} else {
		query = query.Where("c.post_id = ? AND c.parent_id = ?", postID, *parentID)
	}
	query = query.Order("c.created_at ASC, c.id ASC")
	if after != nil {
		query = query.Where(
			"c.created_at > ? OR (c.created_at = ? AND c.id > ?)",
			after.CreatedAt,
			after.CreatedAt,
			after.ID,
		)
	}
	var rows []model.CommentWithAuthor
	if err := query.Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return commentsFromRows(rows), nil
}

func (s *store) CommentDescendants(ctx context.Context, postID string, parentIDs []string, maxDepth int) ([]post.Comment, error) {
	var rows []model.CommentWithAuthor
	err := s.tx.WithContext(ctx).Raw(`
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
		`, postID, parentIDs, postID, maxDepth).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return commentsFromRows(rows), nil
}

func (s *store) FindComment(ctx context.Context, commentID string) (post.Comment, error) {
	var row model.Comment
	if err := s.tx.WithContext(ctx).Where("id = ?", commentID).First(&row).Error; err != nil {
		return post.Comment{}, notFound(err, post.ErrCommentNotFound)
	}
	return commentFromRow(row), nil
}

func (s *store) FindPostComment(ctx context.Context, postID, commentID string) (post.Comment, error) {
	var row model.Comment
	if err := s.tx.WithContext(ctx).First(&row, "id = ? AND post_id = ?", commentID, postID).Error; err != nil {
		return post.Comment{}, notFound(err, post.ErrCommentNotFound)
	}
	return commentFromRow(row), nil
}

func (s *store) ShareLockCommentPost(ctx context.Context, postID string) (post.CommentPost, error) {
	var row model.Post
	if err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
		Select("id, status, comments_enabled").First(&row, "id = ?", postID).Error; err != nil {
		return post.CommentPost{}, notFound(err, post.ErrPostNotFound)
	}
	return post.CommentPost{ID: row.ID, Status: post.Status(row.Status), CommentsEnabled: row.CommentsEnabled}, nil
}

func (s *store) LockCommentPost(ctx context.Context, postID string) (post.CommentPost, error) {
	var row model.Post
	if err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id, status, comments_enabled").Where("id = ?", postID).First(&row).Error; err != nil {
		return post.CommentPost{}, notFound(err, post.ErrPostNotFound)
	}
	return post.CommentPost{ID: row.ID, Status: post.Status(row.Status), CommentsEnabled: row.CommentsEnabled}, nil
}

func (s *store) LockComment(ctx context.Context, commentID string) (post.Comment, error) {
	var row model.Comment
	if err := s.tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", commentID).
		First(&row).Error; err != nil {
		return post.Comment{}, notFound(err, post.ErrCommentNotFound)
	}
	return commentFromRow(row), nil
}

func (s *store) LockPostComment(ctx context.Context, postID, commentID string) (post.Comment, error) {
	var row model.Comment
	if err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ? AND post_id = ?", commentID, postID).First(&row).Error; err != nil {
		return post.Comment{}, notFound(err, post.ErrCommentNotFound)
	}
	return commentFromRow(row), nil
}

func (s *store) CommentDepth(ctx context.Context, postID, commentID string, maxDepth int) (int, error) {
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
	if err := s.tx.WithContext(ctx).Raw(query, commentID, postID, postID, maxDepth).Scan(&depth).Error; err != nil {
		return 0, err
	}
	return depth, nil
}

func (s *store) CommentPostID(ctx context.Context, commentID string) (string, error) {
	var target struct {
		PostID string `gorm:"column:post_id"`
	}
	if err := s.tx.WithContext(ctx).Table("comment").Select("post_id").Where("id = ?", commentID).Take(&target).Error; err != nil {
		return "", notFound(err, post.ErrCommentNotFound)
	}
	return target.PostID, nil
}

func (s *store) InsertComment(ctx context.Context, comment *post.Comment) error {
	row := commentRow(*comment)
	if err := s.tx.WithContext(ctx).Create(&row).Error; err != nil {
		return err
	}
	*comment = commentFromRow(row)
	return nil
}

func (s *store) UpdateCommentContent(ctx context.Context, comment post.Comment, content string) error {
	row := commentRow(comment)
	return s.tx.WithContext(ctx).Model(&row).Update("content", content).Error
}

func (s *store) SoftDeleteComment(ctx context.Context, comment post.Comment, content string) error {
	row := commentRow(comment)
	return s.tx.WithContext(ctx).Model(&row).Updates(structured.Fields{
		"is_deleted": true,
		"content":    content,
	}).Error
}
