package series

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/domainerr"
)

func isSlugAvailable(ctx context.Context, store Store, slug, excludeID string) (bool, error) {
	if slug == "" {
		return true, nil
	}
	taken, err := store.SlugTaken(ctx, slug, excludeID)
	return !taken, err
}

func ensureSlugAvailable(ctx context.Context, store Store, slug, excludeID string) error {
	available, err := isSlugAvailable(ctx, store, slug, excludeID)
	if err != nil {
		return err
	}
	if !available {
		return domainerr.Duplicate("series", "slug", slug)
	}
	return nil
}
