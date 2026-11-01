package og

import (
	"context"
	stderrors "errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/mutation"
)

const (
	UnboundAssetRetention  = 30 * 24 * time.Hour
	TerminalAssetRetention = 24 * time.Hour
)

// Cleanup marks OG assets no generation or entity still uses for deletion.
type Cleanup struct {
	units mutation.UnitOfWork[Store]
}

// NewCleanup returns the OG asset cleanup; an asset any entity row still
// points at is never marked for deletion.
func NewCleanup(units mutation.UnitOfWork[Store]) *Cleanup {
	if units == nil {
		panic("OG cleanup: units are required")
	}
	return &Cleanup{units: units}
}

func (s *Cleanup) MarkUnboundReadyAssets(ctx context.Context, cutoff, now time.Time) error {
	var assetIDs []string
	if err := s.units.Read(ctx, func(store Store) (err error) {
		assetIDs, err = store.UnboundReadyAssetIDs(ctx, cutoff)
		return err
	}); err != nil {
		return fmt.Errorf("list unbound OG assets: %w", err)
	}
	return markAssets(ctx, assetIDs, func(ctx context.Context, assetID string) (bool, error) {
		return s.markUnboundReadyAsset(ctx, assetID, cutoff, now)
	}, "Marked unbound OG assets for deletion")
}

func (s *Cleanup) markUnboundReadyAsset(ctx context.Context, assetID string, cutoff, now time.Time) (bool, error) {
	changed := false
	err := s.units.Transaction(ctx, false, func(tx mutation.Tx[Store]) error {
		store := tx.Store()
		asset, found, err := store.LockAsset(ctx, assetID)
		if err != nil || !found {
			return err
		}
		if asset.Kind != assetKindOG || asset.Status != AssetReady || asset.ReadyAt == nil || !asset.ReadyAt.Before(cutoff) {
			return nil
		}
		retained, err := assetRetained(ctx, store, asset)
		if err != nil || retained {
			return err
		}
		changed, err = store.MarkUnboundAssetDeletePending(ctx, asset.ID, cutoff, now)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("mark unbound OG asset %s for deletion: %w", assetID, err)
	}
	return changed, nil
}

func (s *Cleanup) MarkExpiredTerminalGenerationAssets(ctx context.Context, cutoff, now time.Time) error {
	var assetIDs []string
	if err := s.units.Read(ctx, func(store Store) (err error) {
		assetIDs, err = store.ExpiredTerminalAssetIDs(ctx, cutoff)
		return err
	}); err != nil {
		return fmt.Errorf("list expired terminal OG generation assets: %w", err)
	}
	return markAssets(ctx, assetIDs, func(ctx context.Context, assetID string) (bool, error) {
		return s.markExpiredTerminalGenerationAsset(ctx, assetID, cutoff, now)
	}, "Marked terminal OG generation assets for deletion")
}

func (s *Cleanup) markExpiredTerminalGenerationAsset(ctx context.Context, assetID string, cutoff, now time.Time) (bool, error) {
	changed := false
	err := s.units.Transaction(ctx, false, func(tx mutation.Tx[Store]) error {
		store := tx.Store()
		asset, found, err := store.LockAsset(ctx, assetID)
		if err != nil || !found {
			return err
		}
		if asset.Kind != assetKindOG || !asset.CreatedAt.Before(cutoff) ||
			(asset.Status != AssetAllocated && asset.Status != AssetFailed) {
			return nil
		}
		generation, err := store.Generation(ctx, asset.ID)
		if stderrors.Is(err, ErrGenerationNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		switch generation.Status {
		case StatusFailed, StatusSuperseded, StatusCancelled:
		default:
			return nil
		}
		retained, err := assetRetained(ctx, store, asset)
		if err != nil || retained {
			return err
		}
		changed, err = store.MarkTerminalAssetDeletePending(ctx, asset.ID, cutoff, now)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("mark terminal OG generation asset %s for deletion: %w", assetID, err)
	}
	return changed, nil
}

// assetRetained reports whether a binding or an entity row still uses asset.
func assetRetained(ctx context.Context, store Store, asset Asset) (bool, error) {
	bound, err := store.AssetBound(ctx, asset.ID)
	if err != nil || bound {
		return bound, err
	}
	return AssetProtected(ctx, store, asset)
}

// AssetProtected reports whether an entity row still points at an OG asset,
// which keeps it from deletion while no binding holds it.
func AssetProtected(ctx context.Context, store Store, asset Asset) (bool, error) {
	if asset.Kind != assetKindOG {
		return false, nil
	}
	return store.AssetReferenced(ctx, asset.ID)
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
