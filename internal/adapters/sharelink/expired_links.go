package sharelinkadapter

import (
	"context"
	"time"

	"gorm.io/gorm"

	sharelinkpostgres "github.com/echovisionlab/geul-api/internal/sharelink/postgres"
)

// DeleteExpiredLinks deletes the share links that expired before now and
// returns how many it deleted.
func DeleteExpiredLinks(ctx context.Context, db *gorm.DB, now time.Time) (int64, error) {
	return sharelinkpostgres.NewStore(db).DeleteExpiredLinks(ctx, now)
}
