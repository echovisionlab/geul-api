package og

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

const (
	UnboundAssetRetention  = 30 * 24 * time.Hour
	TerminalAssetRetention = 24 * time.Hour
)

// AssetReference reports whether one entity family still points at an OG
// asset. The family's owner answers for its own rows.
type AssetReference interface {
	ReferencesAsset(ctx context.Context, tx *gorm.DB, assetID string) (bool, error)
}

type Cleanup struct {
	db         *gorm.DB
	references []AssetReference
}

// NewCleanup returns the OG asset cleanup; an asset any of references still
// points at is never marked for deletion.
func NewCleanup(db *gorm.DB, references ...AssetReference) *Cleanup {
	if db == nil {
		panic("OG cleanup: database is required")
	}
	return &Cleanup{db: db, references: references}
}

func (s *Cleanup) MarkExpiredAssets(ctx context.Context, now time.Time) error {
	unboundErr := s.MarkUnboundReadyAssets(ctx, now.Add(-UnboundAssetRetention), now)
	terminalErr := s.MarkExpiredTerminalGenerationAssets(ctx, now.Add(-TerminalAssetRetention), now)
	return stderrors.Join(unboundErr, terminalErr)
}

func (s *Cleanup) MarkUnboundReadyAssets(ctx context.Context, cutoff, now time.Time) error {
	var assetIDs []string
	if err := s.db.WithContext(ctx).Model(&model.PublicAsset{}).
		Where("kind = ? AND status = ? AND ready_at < ?", "og", model.PublicAssetStatusReady, cutoff).
		Where("NOT EXISTS (SELECT 1 FROM public_asset_binding pab WHERE pab.asset_id = public_asset.id)").
		Order("ready_at ASC, id ASC").Pluck("id", &assetIDs).Error; err != nil {
		return fmt.Errorf("list unbound OG assets: %w", err)
	}
	return markAssets(ctx, assetIDs, func(ctx context.Context, assetID string) (bool, error) {
		return s.MarkUnboundReadyAsset(ctx, assetID, cutoff, now, nil)
	}, "Marked unbound OG assets for deletion")
}

func (s *Cleanup) MarkUnboundReadyAsset(
	ctx context.Context,
	assetID string,
	cutoff, now time.Time,
	afterLock func(),
) (bool, error) {
	changed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var asset model.PublicAsset
		if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Select("id", "kind", "status", "ready_at").Where("id = ?", assetID).Take(&asset).Error; err != nil {
			if stderrors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if asset.Kind != "og" || asset.Status != model.PublicAssetStatusReady || asset.ReadyAt == nil || !asset.ReadyAt.Before(cutoff) {
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
		protected, err := s.IsPublicAssetProtected(ctx, tx, asset)
		if err != nil || protected {
			return err
		}
		result := tx.Model(&model.PublicAsset{}).
			Where("id = ? AND kind = ? AND status = ? AND ready_at < ?", asset.ID, "og", model.PublicAssetStatusReady, cutoff).
			Updates(structured.Fields{"status": model.PublicAssetStatusDeletePending, "delete_requested_at": now, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		changed = result.RowsAffected == 1
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("mark unbound OG asset %s for deletion: %w", assetID, err)
	}
	return changed, nil
}

func (s *Cleanup) MarkExpiredTerminalGenerationAssets(ctx context.Context, cutoff, now time.Time) error {
	var assetIDs []string
	if err := s.db.WithContext(ctx).Table("public_asset").Select("public_asset.id").
		Joins("JOIN og_generation ON og_generation.id = public_asset.id").
		Where("public_asset.kind = ?", "og").
		Where("public_asset.status IN ?", []string{model.PublicAssetStatusAllocated, model.PublicAssetStatusFailed}).
		Where("public_asset.created_at < ?", cutoff).
		Where("og_generation.status IN ?", []string{
			model.OgGenerationStatusFailed, model.OgGenerationStatusSuperseded, model.OgGenerationStatusCancelled,
		}).Order("public_asset.created_at ASC, public_asset.id ASC").Pluck("public_asset.id", &assetIDs).Error; err != nil {
		return fmt.Errorf("list expired terminal OG generation assets: %w", err)
	}
	return markAssets(ctx, assetIDs, func(ctx context.Context, assetID string) (bool, error) {
		return s.MarkExpiredTerminalGenerationAsset(ctx, assetID, cutoff, now, nil)
	}, "Marked terminal OG generation assets for deletion")
}

func (s *Cleanup) MarkExpiredTerminalGenerationAsset(
	ctx context.Context,
	assetID string,
	cutoff, now time.Time,
	afterLock func(),
) (bool, error) {
	changed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var asset model.PublicAsset
		if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Select("id", "kind", "status", "created_at").Where("id = ?", assetID).Take(&asset).Error; err != nil {
			if stderrors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if asset.Kind != "og" || !asset.CreatedAt.Before(cutoff) ||
			(asset.Status != model.PublicAssetStatusAllocated && asset.Status != model.PublicAssetStatusFailed) {
			return nil
		}
		if afterLock != nil {
			afterLock()
		}
		var generation model.OgGeneration
		if err := tx.Select("id", "status").First(&generation, "id = ?", asset.ID).Error; err != nil {
			if stderrors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		switch generation.Status {
		case model.OgGenerationStatusFailed, model.OgGenerationStatusSuperseded, model.OgGenerationStatusCancelled:
		default:
			return nil
		}
		var bindings int64
		if err := tx.Model(&model.PublicAssetBinding{}).Where("asset_id = ?", asset.ID).Count(&bindings).Error; err != nil {
			return err
		}
		if bindings != 0 {
			return nil
		}
		protected, err := s.IsPublicAssetProtected(ctx, tx, asset)
		if err != nil || protected {
			return err
		}
		result := tx.Model(&model.PublicAsset{}).
			Where("id = ? AND kind = ? AND status IN ? AND created_at < ?", asset.ID, "og", []string{
				model.PublicAssetStatusAllocated, model.PublicAssetStatusFailed,
			}, cutoff).
			Updates(structured.Fields{"status": model.PublicAssetStatusDeletePending, "delete_requested_at": now, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		changed = result.RowsAffected == 1
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("mark terminal OG generation asset %s for deletion: %w", assetID, err)
	}
	return changed, nil
}

func (s *Cleanup) IsPublicAssetProtected(ctx context.Context, tx *gorm.DB, asset model.PublicAsset) (bool, error) {
	if asset.Kind != "og" {
		return false, nil
	}
	for _, reference := range s.references {
		referenced, err := reference.ReferencesAsset(ctx, tx, asset.ID)
		if err != nil || referenced {
			return referenced, err
		}
	}
	return false, nil
}

func markAssets(
	ctx context.Context,
	assetIDs []string,
	mark func(context.Context, string) (bool, error),
	message string,
) error {
	marked := 0
	var cleanupErrs []error
	for _, assetID := range assetIDs {
		changed, err := mark(ctx, assetID)
		if err != nil {
			cleanupErrs = append(cleanupErrs, err)
			continue
		}
		if changed {
			marked++
		}
	}
	if marked > 0 {
		slog.InfoContext(ctx, message, "count", marked)
	}
	return stderrors.Join(cleanupErrs...)
}
