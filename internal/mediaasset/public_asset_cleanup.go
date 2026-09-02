package mediaasset

import (
	"context"
	stderrors "errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
)

const (
	UnreadyPublicAssetRetention = 24 * time.Hour
	publicAssetPurgeBatchSize   = 30
)

type PublicAssetProtector interface {
	IsPublicAssetProtected(context.Context, *gorm.DB, model.PublicAsset) (bool, error)
}

type PublicAssetCache interface {
	Prefix(model.PublicAsset) (string, error)
	PurgePrefixes(context.Context, []string) error
}

type PublicAssetCleanup struct {
	db        *gorm.DB
	objects   CleanupObjectStore
	cache     PublicAssetCache
	protector PublicAssetProtector
}

func NewPublicAssetCleanup(
	db *gorm.DB,
	objects CleanupObjectStore,
	cache PublicAssetCache,
	protector PublicAssetProtector,
) *PublicAssetCleanup {
	if db == nil || objects == nil || cache == nil || protector == nil {
		panic("public asset cleanup dependencies are required")
	}
	return &PublicAssetCleanup{db: db, objects: objects, cache: cache, protector: protector}
}

type pendingPublicAssetDeletion struct {
	asset  model.PublicAsset
	prefix string
}

func (s *PublicAssetCleanup) DeletePending(ctx context.Context, now time.Time) error {
	var assets []model.PublicAsset
	if err := s.db.WithContext(ctx).
		Where("status = ?", model.PublicAssetStatusDeletePending).
		Order("delete_requested_at ASC, id ASC").
		Find(&assets).Error; err != nil {
		return fmt.Errorf("list pending public assets: %w", err)
	}

	var cleanupErrs []error
	targets := make([]pendingPublicAssetDeletion, 0, len(assets))
	for _, asset := range assets {
		target, err := s.preparePendingDeletion(ctx, asset)
		if err != nil {
			cleanupErrs = append(cleanupErrs, err)
			continue
		}
		targets = append(targets, target)
	}
	for start := 0; start < len(targets); start += publicAssetPurgeBatchSize {
		end := min(start+publicAssetPurgeBatchSize, len(targets))
		batch := targets[start:end]
		prefixes := make([]string, 0, len(batch))
		for _, target := range batch {
			prefixes = append(prefixes, target.prefix)
		}
		if err := s.cache.PurgePrefixes(ctx, prefixes); err != nil {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("purge %d public assets: %w", len(batch), err))
			continue
		}
		for _, target := range batch {
			if err := s.finalizePendingDeletion(ctx, target.asset, now); err != nil {
				cleanupErrs = append(cleanupErrs, err)
			}
		}
	}
	return stderrors.Join(cleanupErrs...)
}

func (s *PublicAssetCleanup) preparePendingDeletion(ctx context.Context, asset model.PublicAsset) (pendingPublicAssetDeletion, error) {
	var bindings int64
	if err := s.db.WithContext(ctx).Model(&model.PublicAssetBinding{}).
		Where("asset_id = ?", asset.ID).Count(&bindings).Error; err != nil {
		return pendingPublicAssetDeletion{}, fmt.Errorf("check public asset bindings %s: %w", asset.ID, err)
	}
	if bindings != 0 {
		return pendingPublicAssetDeletion{}, fmt.Errorf("delete-pending public asset %s still has bindings", asset.ID)
	}
	protected, err := s.protector.IsPublicAssetProtected(ctx, s.db.WithContext(ctx), asset)
	if err != nil {
		return pendingPublicAssetDeletion{}, fmt.Errorf("check delete-pending public asset %s: %w", asset.ID, err)
	}
	if protected {
		return pendingPublicAssetDeletion{}, fmt.Errorf("delete-pending public asset %s is still protected", asset.ID)
	}
	if err := s.objects.DeleteObject(ctx, asset.ObjectKey); err != nil {
		return pendingPublicAssetDeletion{}, fmt.Errorf("delete public asset object %s: %w", asset.ObjectKey, err)
	}
	prefix, err := s.cache.Prefix(asset)
	if err != nil {
		return pendingPublicAssetDeletion{}, fmt.Errorf("build cache prefix for public asset %s: %w", asset.ID, err)
	}
	return pendingPublicAssetDeletion{asset: asset, prefix: prefix}, nil
}

func (s *PublicAssetCleanup) finalizePendingDeletion(ctx context.Context, asset model.PublicAsset, now time.Time) error {
	result := s.db.WithContext(ctx).Model(&model.PublicAsset{}).
		Where("id = ? AND status = ?", asset.ID, model.PublicAssetStatusDeletePending).
		Updates(structured.Fields{"status": model.PublicAssetStatusDeleted, "deleted_at": now, "updated_at": now})
	if result.Error != nil {
		return fmt.Errorf("mark public asset deleted %s: %w", asset.ID, result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("public asset %s changed during deletion", asset.ID)
	}
	return nil
}

func (s *PublicAssetCleanup) DeleteExpiredUnready(ctx context.Context, cutoff time.Time) error {
	var assetIDs []string
	if err := s.db.WithContext(ctx).Model(&model.PublicAsset{}).
		Where("status IN ? AND created_at < ?", []string{model.PublicAssetStatusAllocated, model.PublicAssetStatusFailed}, cutoff).
		Where("NOT EXISTS (SELECT 1 FROM og_generation WHERE og_generation.id = public_asset.id)").
		Order("created_at ASC, id ASC").Pluck("id", &assetIDs).Error; err != nil {
		return fmt.Errorf("list expired unready public assets: %w", err)
	}
	var cleanupErrs []error
	for _, assetID := range assetIDs {
		if err := s.deleteExpiredUnready(ctx, assetID, cutoff, nil); err != nil {
			cleanupErrs = append(cleanupErrs, err)
		}
	}
	return stderrors.Join(cleanupErrs...)
}

func (s *PublicAssetCleanup) deleteExpiredUnready(ctx context.Context, assetID string, cutoff time.Time, afterLock func()) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var asset model.PublicAsset
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "kind", "status", "object_key", "created_at").
			Where("id = ?", assetID).Take(&asset).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return nil
			}
			return fmt.Errorf("lock expired public asset %s: %w", assetID, err)
		}
		if (asset.Status != model.PublicAssetStatusAllocated && asset.Status != model.PublicAssetStatusFailed) ||
			!asset.CreatedAt.Before(cutoff) {
			return nil
		}
		var generationCount int64
		if err := tx.Model(&model.OgGeneration{}).Where("id = ?", asset.ID).Count(&generationCount).Error; err != nil {
			return fmt.Errorf("recheck expired public asset generation %s: %w", asset.ID, err)
		}
		if generationCount != 0 {
			return nil
		}
		if afterLock != nil {
			afterLock()
		}
		if err := s.objects.DeleteObject(ctx, asset.ObjectKey); err != nil {
			return fmt.Errorf("delete expired public asset object %s: %w", asset.ObjectKey, err)
		}
		result := tx.Where("id = ? AND status IN ? AND created_at < ?", asset.ID, []string{
			model.PublicAssetStatusAllocated, model.PublicAssetStatusFailed,
		}, cutoff).Delete(&model.PublicAsset{})
		if result.Error != nil {
			return fmt.Errorf("delete expired public asset row %s: %w", asset.ID, result.Error)
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("public asset %s changed during unready cleanup", asset.ID)
		}
		return nil
	})
}
