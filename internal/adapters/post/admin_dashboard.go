package post

import (
	"context"

	"gorm.io/gorm"
)

// DashboardPosts counts Posts and comments for the administrator dashboard.
type DashboardPosts struct{}

func (DashboardPosts) CountPosts(ctx context.Context, db *gorm.DB) (int64, error) {
	var count int64
	err := db.WithContext(ctx).Table("post").Count(&count).Error
	return count, err
}

// CountComments counts the comments that are not deleted.
func (DashboardPosts) CountComments(ctx context.Context, db *gorm.DB) (int64, error) {
	var count int64
	err := db.WithContext(ctx).Table("comment").Where("is_deleted = false").Count(&count).Error
	return count, err
}
