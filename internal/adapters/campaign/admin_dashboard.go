package campaign

import (
	"context"

	"gorm.io/gorm"
)

// DashboardCampaigns counts Campaigns for the administrator dashboard.
type DashboardCampaigns struct{}

func (DashboardCampaigns) CountCampaigns(ctx context.Context, db *gorm.DB) (int64, error) {
	var count int64
	err := db.WithContext(ctx).Table("campaign").Count(&count).Error
	return count, err
}
