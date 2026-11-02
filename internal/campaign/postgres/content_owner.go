package campaignpostgres

import (
	"context"

	"gorm.io/gorm"
)

// CampaignContentOwner supplies the already locked content owner's row query.
func CampaignContentOwner(ctx context.Context, tx *gorm.DB, id string) *gorm.DB {
	return tx.WithContext(ctx).Table("campaign AS owner").Where("owner.id = ?", id)
}
