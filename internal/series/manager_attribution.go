package series

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// series_manager is a durable product attribution used for participant
// display and history. SpiceDB owns the independent current manager grant.
func upsertSeriesManagerAttribution(
	ctx context.Context,
	db *gorm.DB,
	seriesID string,
	memberID string,
	updatedAt time.Time,
) error {
	return db.WithContext(ctx).Exec(`
INSERT INTO series_manager (series_id, member_id, created_at, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT (series_id, member_id)
DO UPDATE SET updated_at = EXCLUDED.updated_at
`, seriesID, memberID, updatedAt, updatedAt).Error
}

func deleteSeriesManagerAttribution(ctx context.Context, db *gorm.DB, seriesID, memberID string) error {
	return db.WithContext(ctx).
		Exec("DELETE FROM series_manager WHERE series_id = ? AND member_id = ?", seriesID, memberID).
		Error
}

func seriesManagerAttributionExists(ctx context.Context, tx *gorm.DB, seriesID, memberID string) (bool, error) {
	var count int64
	if err := tx.WithContext(ctx).Table("series_manager").
		Where("series_id = ? AND member_id = ?", seriesID, memberID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count != 0, nil
}
