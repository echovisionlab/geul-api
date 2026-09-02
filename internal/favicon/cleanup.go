package favicon

import (
	"context"
	stderrors "errors"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
)

const DanglingAssetRetention = 24 * time.Hour

type Cleanup struct {
	db *gorm.DB
}

func NewCleanup(db *gorm.DB) *Cleanup {
	if db == nil {
		panic("favicon cleanup: database is required")
	}
	return &Cleanup{db: db}
}

func (s *Cleanup) MarkDanglingReadyAssets(ctx context.Context, cutoff, now time.Time) error {
	var assetIDs []string
	if err := s.db.WithContext(ctx).Model(&model.PublicAsset{}).
		Where("kind = ? AND status = ? AND created_at < ?", "favicon", model.PublicAssetStatusReady, cutoff).
		Where("NOT EXISTS (SELECT 1 FROM public_asset_binding pab WHERE pab.asset_id = public_asset.id)").
		Order("created_at ASC, id ASC").Pluck("id", &assetIDs).Error; err != nil {
		return fmt.Errorf("list dangling favicon assets: %w", err)
	}
	marked := 0
	var cleanupErrs []error
	for _, assetID := range assetIDs {
		changed, err := s.MarkDanglingReadyAsset(ctx, assetID, cutoff, now, nil)
		if err != nil {
			cleanupErrs = append(cleanupErrs, err)
			continue
		}
		if changed {
			marked++
		}
	}
	if marked > 0 {
		slog.Info("Marked dangling favicon assets for deletion", "count", marked)
	}
	return stderrors.Join(cleanupErrs...)
}

func (s *Cleanup) MarkDanglingReadyAsset(
	ctx context.Context,
	assetID string,
	cutoff, now time.Time,
	afterLock func(),
) (bool, error) {
	changed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var asset model.PublicAsset
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "kind", "status", "created_at").Where("id = ?", assetID).Take(&asset).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return nil
			}
			return err
		}
		if asset.Kind != "favicon" || asset.Status != model.PublicAssetStatusReady || !asset.CreatedAt.Before(cutoff) {
			return nil
		}
		if afterLock != nil {
			afterLock()
		}
		var bindings int64
		if err := tx.Model(&model.PublicAssetBinding{}).Where("asset_id = ?", asset.ID).Count(&bindings).Error; err != nil {
			return err
		}
		if bindings != 0 {
			return nil
		}
		result := tx.Model(&model.PublicAsset{}).
			Where("id = ? AND status = ?", asset.ID, model.PublicAssetStatusReady).
			Updates(structured.Fields{"status": model.PublicAssetStatusDeletePending, "delete_requested_at": now, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		changed = result.RowsAffected == 1
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("mark dangling favicon asset %s for deletion: %w", assetID, err)
	}
	return changed, nil
}
