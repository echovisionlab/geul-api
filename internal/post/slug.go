package post

import (
	"context"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
)

// CheckSlugAvailable reports whether slug is unclaimed by both a Post and the
// shared Page route namespace. excludePostID is the Post currently being
// edited and must already have passed authorization at the transport boundary.
func CheckSlugAvailable(ctx context.Context, db *gorm.DB, slug, excludePostID string) (bool, error) {
	if strings.Contains(slug, "/") {
		return false, domainerr.InvalidField("slug", "must not contain '/'")
	}

	query := db.WithContext(ctx).Model(&model.Post{}).Where("slug = ?", slug)
	if excludePostID != "" {
		query = query.Where("id != ?", excludePostID)
	}

	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	if count != 0 {
		return false, nil
	}

	available, err := routeregistry.IsResourceRouteAvailable(ctx, db, "posts", slug)
	if err != nil {
		return false, err
	}
	return available, nil
}
