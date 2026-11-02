package programeventpostgres

import (
	"context"

	"gorm.io/gorm"
)

// MapPlaceInUse checks the references this owner keeps for a catalog deletion.
func MapPlaceInUse(ctx context.Context, tx *gorm.DB, id string) (bool, error) {
	var count int64
	err := tx.WithContext(ctx).Table("program_event").Where("map_place_id = ?", id).Limit(1).Count(&count).Error
	return count > 0, err
}

// ClientInUse checks the references this owner keeps for a catalog deletion.
func ClientInUse(ctx context.Context, tx *gorm.DB, id string) (bool, error) {
	var count int64
	err := tx.WithContext(ctx).Table("program_event_client").Where("client_id = ?", id).Limit(1).Count(&count).Error
	return count > 0, err
}
