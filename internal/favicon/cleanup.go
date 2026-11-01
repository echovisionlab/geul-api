package favicon

import (
	"context"
	stderrors "errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/mutation"
)

const DanglingAssetRetention = 24 * time.Hour

type Cleanup struct {
	units mutation.UnitOfWork[Store]
}

func NewCleanup(units mutation.UnitOfWork[Store]) *Cleanup {
	if units == nil {
		panic("favicon cleanup: units are required")
	}
	return &Cleanup{units: units}
}

func (s *Cleanup) MarkDanglingReadyAssets(ctx context.Context, cutoff, now time.Time) error {
	var assetIDs []string
	if err := s.units.Read(ctx, func(store Store) (err error) {
		assetIDs, err = store.DanglingReadyAssetIDs(ctx, cutoff)
		return err
	}); err != nil {
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
		slog.InfoContext(ctx, "Marked dangling favicon assets for deletion", "count", marked)
	}
	return stderrors.Join(cleanupErrs...)
}

// MarkDanglingReadyAsset marks one favicon asset nothing binds for deletion
// once it is older than cutoff; afterLock runs once the asset is locked.
func (s *Cleanup) MarkDanglingReadyAsset(
	ctx context.Context,
	assetID string,
	cutoff, now time.Time,
	afterLock func(),
) (bool, error) {
	changed := false
	err := s.units.Transaction(ctx, false, func(tx mutation.Tx[Store]) error {
		store := tx.Store()
		asset, found, err := store.LockAsset(ctx, assetID)
		if err != nil || !found {
			return err
		}
		if asset.Kind != "favicon" || asset.Status != AssetReady || !asset.CreatedAt.Before(cutoff) {
			return nil
		}
		if afterLock != nil {
			afterLock()
		}
		bound, err := store.AssetBound(ctx, asset.ID)
		if err != nil || bound {
			return err
		}
		changed, err = store.MarkAssetDeletePending(ctx, asset.ID, now)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("mark dangling favicon asset %s for deletion: %w", assetID, err)
	}
	return changed, nil
}
