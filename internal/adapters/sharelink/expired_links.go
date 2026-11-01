package sharelinkadapter

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
)

// DeleteExpiredLinks deletes the share links that expired before now and
// returns how many it deleted.
func DeleteExpiredLinks(ctx context.Context, db *gorm.DB, now time.Time) (int64, error) {
	result := db.WithContext(ctx).Where("expires_at < ?", now).Delete(&model.ShareLink{})
	return result.RowsAffected, result.Error
}
