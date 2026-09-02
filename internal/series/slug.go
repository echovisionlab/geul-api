package series

import (
	"context"

	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/structured"
)

func isSlugAvailable(ctx context.Context, db *gorm.DB, value structured.Value, slug, excludeID string) (bool, error) {
	if slug == "" {
		return true, nil
	}
	query := db.WithContext(ctx).Model(value).Where("slug = ?", slug)
	if excludeID != "" {
		query = query.Where("id != ?", excludeID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, errs.Internal(err)
	}
	return count == 0, nil
}

func ensureSlugAvailable(ctx context.Context, db *gorm.DB, value structured.Value, entity, slug, excludeID string) error {
	available, err := isSlugAvailable(ctx, db, value, slug, excludeID)
	if err != nil {
		return err
	}
	if !available {
		return errs.SlugAlreadyExists(entity, slug)
	}
	return nil
}
