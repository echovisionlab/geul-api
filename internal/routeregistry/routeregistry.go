// Package routeregistry owns the site-route namespace shared by Pages and
// resource detail routes.
package routeregistry

import (
	"context"
	"strings"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"gorm.io/gorm"
)

var fixedPageRouteNamespaces = map[string]struct{}{
	"_next": {}, "account": {}, "admin": {}, "api": {}, "auth": {}, "category": {},
	"changelog": {}, "favicon.ico": {}, "files": {}, "login": {},
	"manifest.webmanifest": {}, "my": {}, "onboarding": {}, "privacy": {},
	"robots.txt": {}, "s": {}, "sitemap": {}, "sitemap.xml": {}, "sitemaps": {},
	"subscribe": {}, "tag": {}, "terms": {}, "tools": {}, "unsubscribe": {},
	"user": {}, "verification": {}, "verify": {},
}

// These exact roots are CMS Pages while their child paths remain app-owned.
var cmsPageRootExceptions = map[string]struct{}{
	"tools": {},
}

type pageRouteResource struct {
	table   string
	hasSlug bool
}

// Resource routes are one segment below their plural prefix. A Page may use
// the same prefix when no current resource owns that exact route.
var pageRouteResources = map[string]pageRouteResource{
	"campaigns":    {table: "campaign", hasSlug: false},
	"event-series": {table: "program_event_series", hasSlug: true},
	"events":       {table: "program_event", hasSlug: true},
	"forms":        {table: "form", hasSlug: true},
	"posts":        {table: "post", hasSlug: true},
	"series":       {table: "series", hasSlug: true},
	"works":        {table: "work", hasSlug: true},
}

func pageSlugRoot(slug string) string {
	root, _, _ := strings.Cut(slug, "/")
	return strings.ToLower(root)
}

// IsReservedPagePath reports whether the first route segment is reserved by a
// fixed site route.
func IsReservedPagePath(slug string) bool {
	slug = strings.TrimSpace(slug)
	root := pageSlugRoot(slug)
	if _, reserved := fixedPageRouteNamespaces[root]; reserved {
		if _, cmsRoot := cmsPageRootExceptions[root]; cmsRoot && !strings.Contains(slug, "/") {
			return false
		}
		return true
	}
	// Resource detail routes are conditionally occupied by a current entity.
	// The database-backed collision check runs after this structural check.
	return false
}

func pageRouteResourceForSlug(slug string) (pageRouteResource, string, bool) {
	segments := strings.Split(strings.TrimSpace(slug), "/")
	if len(segments) != 2 {
		return pageRouteResource{}, "", false
	}
	resource, ok := pageRouteResources[strings.ToLower(segments[0])]
	if !ok || segments[1] == "" {
		return pageRouteResource{}, "", false
	}
	return resource, segments[1], true
}

// IsPageRouteOccupiedByResource reports whether a current resource owns the
// given detail route.
func IsPageRouteOccupiedByResource(ctx context.Context, db *gorm.DB, slug string) (bool, error) {
	resource, tail, ok := pageRouteResourceForSlug(slug)
	if !ok {
		return false, nil
	}
	query := db.WithContext(ctx).Table(resource.table).Where("CAST(id AS TEXT) = ?", tail)
	if resource.hasSlug {
		query = query.Or("slug = ?", tail)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, errs.Internal(err)
	}
	return count > 0, nil
}

func isPageSlugOccupiedByResource(ctx context.Context, db *gorm.DB, namespace, slug string) (bool, error) {
	if strings.TrimSpace(slug) == "" {
		return false, nil
	}
	var count int64
	if err := db.WithContext(ctx).
		Table("page").
		Where("slug = ?", strings.TrimSuffix(namespace, "/")+"/"+slug).
		Count(&count).Error; err != nil {
		return false, errs.Internal(err)
	}
	return count > 0, nil
}

// LockPageRouteConflict serializes a Page route and a resource detail route
// that may occupy the same path. It is a no-op outside PostgreSQL.
func LockPageRouteConflict(ctx context.Context, tx *gorm.DB, slug string) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	if _, _, ok := pageRouteResourceForSlug(slug); !ok {
		return nil
	}
	return tx.WithContext(ctx).Exec(
		"SELECT pg_advisory_xact_lock(hashtextextended(?, 0))",
		slug,
	).Error
}

func lockResourcePageRoute(ctx context.Context, tx *gorm.DB, namespace, slug string) error {
	return LockPageRouteConflict(ctx, tx, strings.TrimSuffix(namespace, "/")+"/"+slug)
}

// EnsureResourceRouteAvailableInTx locks and verifies a resource route in
// one transaction.
func EnsureResourceRouteAvailableInTx(
	ctx context.Context,
	tx *gorm.DB,
	entity string,
	namespace string,
	slug string,
) error {
	if err := lockResourcePageRoute(ctx, tx, namespace, slug); err != nil {
		return err
	}
	return EnsureResourceRouteAvailable(ctx, tx, entity, namespace, slug)
}

// EnsureResourceRouteAvailable verifies that no Page occupies a resource
// detail route.
func EnsureResourceRouteAvailable(
	ctx context.Context,
	db *gorm.DB,
	entity string,
	namespace string,
	slug string,
) error {
	occupied, err := isPageSlugOccupiedByResource(ctx, db, namespace, slug)
	if err != nil {
		return err
	}
	if occupied {
		return errs.SlugAlreadyExists(entity, slug)
	}
	return nil
}

// IsResourceRouteAvailable reports whether no Page occupies a resource
// detail route.
func IsResourceRouteAvailable(ctx context.Context, db *gorm.DB, namespace, slug string) (bool, error) {
	occupied, err := isPageSlugOccupiedByResource(ctx, db, namespace, slug)
	if err != nil {
		return false, err
	}
	return !occupied, nil
}

// ValidatePagePath validates a Page route path against structural and fixed
// route namespace constraints.
func ValidatePagePath(slug string) error {
	if slug == "" || slug != strings.TrimSpace(slug) {
		return errs.InvalidArgument("slug", "must be a non-empty trimmed route path")
	}
	if strings.HasPrefix(slug, "/") || strings.HasSuffix(slug, "/") || strings.Contains(slug, "//") {
		return errs.InvalidArgument("slug", "must not contain an empty route segment")
	}
	for segment := range strings.SplitSeq(slug, "/") {
		if segment == "." || segment == ".." {
			return errs.InvalidArgument("slug", "must not contain dot route segments")
		}
	}
	if IsReservedPagePath(slug) {
		return errs.InvalidArgument("slug", "is reserved by a site route")
	}
	return nil
}
