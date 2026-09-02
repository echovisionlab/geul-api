package model

import (
	"time"
)

// Comment represents a comment on a post
type Comment struct {
	ID        string    `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	PostID    string    `gorm:"column:post_id;type:uuid;not null"`
	MemberID  *string   `gorm:"column:member_id;type:uuid"`
	ParentID  *string   `gorm:"column:parent_id;type:uuid"`
	Content   string    `gorm:"column:content;type:text;not null"`
	IsDeleted bool      `gorm:"column:is_deleted;default:false"`
	CreatedAt time.Time `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null;default:now()"`

	// Associations
	Post   *Post    `gorm:"foreignKey:PostID;references:ID"`
	Member *Member  `gorm:"foreignKey:MemberID;references:ID"`
	Parent *Comment `gorm:"foreignKey:ParentID;references:ID"`
}

func (Comment) TableName() string {
	return "comment"
}

// CommentWithAuthor is used for queries that join user data
type CommentWithAuthor struct {
	ID                  string    `gorm:"column:id"`
	PostID              string    `gorm:"column:post_id"`
	MemberID            *string   `gorm:"column:member_id"`
	ParentID            *string   `gorm:"column:parent_id"`
	Content             string    `gorm:"column:content"`
	IsDeleted           bool      `gorm:"column:is_deleted"`
	CreatedAt           time.Time `gorm:"column:created_at"`
	UpdatedAt           time.Time `gorm:"column:updated_at"`
	AuthorDisplayName   *string   `gorm:"column:author_display_name"`
	AuthorAvatarAssetID *string   `gorm:"column:author_avatar_asset_id"`
}
