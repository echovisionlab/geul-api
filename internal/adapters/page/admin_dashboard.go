package page

import (
	"context"

	"gorm.io/gorm"
)

// DashboardPages counts Pages for the administrator dashboard.
type DashboardPages struct{}

func (DashboardPages) CountPages(ctx context.Context, db *gorm.DB) (int64, error) {
	var count int64
	err := db.WithContext(ctx).Table("page").Count(&count).Error
	return count, err
}
