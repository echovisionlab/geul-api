package label

import (
	"context"
	"strings"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const labelContentEntity = "label"
const creativeContentProfile = "compact"

func IsValidUUID(value string) bool {
	_, err := uuid.Parse(value)
	return err == nil
}

func validateSlugWithoutSlash(slug string) error {
	if strings.Contains(slug, "/") {
		return errs.InvalidArgument("slug", "must not contain '/'")
	}
	return nil
}

func ensureSlugAvailable(ctx context.Context, db *gorm.DB, value structured.Value, entity, slug, excludeID string) error {
	if slug == "" {
		return nil
	}
	query := db.WithContext(ctx).Model(value).Where("slug = ?", slug)
	if excludeID != "" {
		query = query.Where("id != ?", excludeID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return errs.Internal(err)
	}
	if count != 0 {
		return errs.SlugAlreadyExists(entity, slug)
	}
	return nil
}

func normalizedOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		return nil
	}
	return &normalized
}

func normalizeOptionalNullableString(value *string) (*string, bool) {
	if value == nil {
		return nil, false
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil, true
	}
	return &trimmed, true
}

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
