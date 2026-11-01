package admin

import (
	"context"

	"gorm.io/gorm"
)

// Members counts the dashboard's Members; Member owns them.
type Members interface {
	// CountActive counts the live Members whose linked Identity is active
	// and not banned.
	CountActive(ctx context.Context, db *gorm.DB) (int64, error)
}

// Posts counts the dashboard's Posts and comments; Post owns both.
type Posts interface {
	CountPosts(ctx context.Context, db *gorm.DB) (int64, error)
	// CountComments counts the comments that are not deleted.
	CountComments(ctx context.Context, db *gorm.DB) (int64, error)
}

// Pages counts the dashboard's Pages; Page owns them.
type Pages interface {
	CountPages(ctx context.Context, db *gorm.DB) (int64, error)
}

// Campaigns counts the dashboard's Campaigns; Campaign owns them.
type Campaigns interface {
	CountCampaigns(ctx context.Context, db *gorm.DB) (int64, error)
}
