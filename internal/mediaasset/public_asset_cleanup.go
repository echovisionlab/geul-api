package mediaasset

import (
	"context"
	stderrors "errors"
	"fmt"
	"time"

	"github.com/echovisionlab/geul-api/internal/mutation"
)

const (
	UnreadyPublicAssetRetention = 24 * time.Hour
	publicAssetPurgeBatchSize   = 30
)

type PublicAssetProtector interface {
	IsPublicAssetProtected(context.Context, Asset) (bool, error)
}

type PublicAssetCache interface {
	Prefix(Asset) (string, error)
	PurgePrefixes(context.Context, []string) error
}

type PublicAssetCleanup struct {
	units     mutation.UnitOfWork[CleanupStore]
	objects   CleanupObjectStore
	cache     PublicAssetCache
	protector PublicAssetProtector
}

func NewPublicAssetCleanup(
	units mutation.UnitOfWork[CleanupStore],
	objects CleanupObjectStore,
	cache PublicAssetCache,
	protector PublicAssetProtector,
) *PublicAssetCleanup {
	if units == nil || objects == nil || cache == nil || protector == nil {
		panic("public asset cleanup dependencies are required")
	}
	return &PublicAssetCleanup{units: units, objects: objects, cache: cache, protector: protector}
}

type pendingPublicAssetDeletion struct {
	asset  Asset
	prefix string
}

func (s *PublicAssetCleanup) DeletePending(ctx context.Context, now time.Time) error {
	var assets []Asset
	if err := s.units.Read(ctx, func(store CleanupStore) error { var err error; assets, err = store.PendingAssets(ctx); return err }); err != nil {
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

func (s *PublicAssetCleanup) preparePendingDeletion(ctx context.Context, asset Asset) (pendingPublicAssetDeletion, error) {
	var bindings int64
	if err := s.units.Read(ctx, func(store CleanupStore) error {
		var err error
		bindings, err = store.AssetBindingCount(ctx, asset.ID)
		return err
	}); err != nil {
		return pendingPublicAssetDeletion{}, fmt.Errorf("check public asset bindings %s: %w", asset.ID, err)
	}
	if bindings != 0 {
		return pendingPublicAssetDeletion{}, fmt.Errorf("delete-pending public asset %s still has bindings", asset.ID)
	}
	protected, err := s.protector.IsPublicAssetProtected(ctx, asset)
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

func (s *PublicAssetCleanup) finalizePendingDeletion(ctx context.Context, asset Asset, now time.Time) error {
	var changed bool
	err := s.units.Transaction(ctx, false, func(unit mutation.Tx[CleanupStore]) error {
		var err error
		changed, err = unit.Store().MarkAssetDeleted(ctx, asset.ID, now)
		return err
	})
	if err != nil {
		return fmt.Errorf("mark public asset deleted %s: %w", asset.ID, err)
	}
	if !changed {
		return fmt.Errorf("public asset %s changed during deletion", asset.ID)
	}
	return nil
}

func (s *PublicAssetCleanup) DeleteExpiredUnready(ctx context.Context, cutoff time.Time) error {
	var assetIDs []string
	if err := s.units.Read(ctx, func(store CleanupStore) error {
		var err error
		assetIDs, err = store.ExpiredUnreadyAssetIDs(ctx, cutoff)
		return err
	}); err != nil {
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
	return s.units.Transaction(ctx, false, func(unit mutation.Tx[CleanupStore]) error {
		tx := unit.Store()
		asset, err := tx.LockExpiredAsset(ctx, assetID)
		if stderrors.Is(err, ErrAssetNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock expired public asset %s: %w", assetID, err)
		}
		if (asset.Status != AssetAllocated && asset.Status != AssetFailed) ||
			!asset.CreatedAt.Before(cutoff) {
			return nil
		}
		hasGeneration, err := tx.AssetHasGeneration(ctx, asset.ID)
		if err != nil {
			return fmt.Errorf("recheck expired public asset generation %s: %w", asset.ID, err)
		}
		if hasGeneration {
			return nil
		}
		if afterLock != nil {
			afterLock()
		}
		if err := s.objects.DeleteObject(ctx, asset.ObjectKey); err != nil {
			return fmt.Errorf("delete expired public asset object %s: %w", asset.ObjectKey, err)
		}
		deleted, err := tx.DeleteExpiredAsset(ctx, asset.ID, cutoff)
		if err != nil {
			return fmt.Errorf("delete expired public asset row %s: %w", asset.ID, err)
		}
		if !deleted {
			return fmt.Errorf("public asset %s changed during unready cleanup", asset.ID)
		}
		return nil
	})
}
