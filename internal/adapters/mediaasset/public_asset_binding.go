package mediaasset

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// BoundPublicAssetID returns the asset bound to the owner's binding key, or
// false when none is bound.
func BoundPublicAssetID(ctx context.Context, db *gorm.DB, ownerType, ownerID, bindingKey string) (string, bool, error) {
	var binding struct {
		AssetID string `gorm:"column:asset_id"`
	}
	err := db.WithContext(ctx).Table("public_asset_binding").
		Select("asset_id").
		Where("owner_type = ? AND owner_id = ? AND binding_key = ?", ownerType, ownerID, bindingKey).
		Take(&binding).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return binding.AssetID, true, nil
}
